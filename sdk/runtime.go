package autobuild

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Runtime is the orchestrator that connects every provider in an Engine
// into a working agent. It drives the LLM↔tool loop and manages memory,
// skills, context budget, and conversation persistence automatically.
//
// Cold vs warm turns:
//
// First Run on a Conversation triggers full orientation (memory read,
// skill matching). Subsequent Runs on the same Conversation skip
// orientation and reuse loaded state — matching how Claude operates
// within a single conversation.
type Runtime struct {
	engine *Engine
	mode   string
	model  string

	// Configurable behavior
	maxSkills       int
	skillThreshold  float64
	memoryTrigger   MemoryTriggerDetector
	memoryWriter    *InferredMemoryWriter
	safety          SafetyFilter
	tokenizer       Tokenizer
	store           ConversationStore
	sessionContext  SessionContextProvider
	compactor       Compactor
	maxMemoryTokens int
	memoryRootsV2   []MemoryRoot
	entrypointName  string         // file name (e.g. MEMORY.md) prepended per scope; "" = disabled
	recaller        MemoryRecaller // optional LLM filter over the header manifest
	recallMax       int            // cap on number of files surfaced via recall (0 = no cap)
	thinkingBudget  int
	interruptGate   *InterruptGate
}

// Tokenizer estimates token count for a string. Replace the default heuristic
// with a real tokenizer (tiktoken, claude-tokenizer) for accurate budgets.
//
// Minimum implementation: Count. Implement Encode/Decode for accurate
// truncation at token boundaries (required by evictMemoryToTokenBudget).
type Tokenizer interface {
	// Count returns the number of tokens in text.
	Count(text string) int

	// Encode converts text to a token ID sequence.
	// Optional: return nil to fall back to character-boundary truncation.
	Encode(text string) []int

	// Decode converts a token ID sequence back to text.
	// Optional: return "" if Encode is not implemented.
	Decode(tokens []int) string
}

// HeuristicTokenizer is the default — chars/4. Good enough for English.
// Inaccurate for languages with longer tokens (Spanish, German) or shorter
// (Chinese, Japanese). Replace for production.
type HeuristicTokenizer struct{}

func (HeuristicTokenizer) Count(text string) int    { return len(text) / 4 }
func (HeuristicTokenizer) Encode(text string) []int { return nil }
func (HeuristicTokenizer) Decode(tokens []int) string { return "" }

// TruncateToTokens truncates text to at most maxTokens tokens using the given
// tokenizer. If Encode returns nil (heuristic tokenizer), falls back to
// character-boundary truncation at maxTokens*4 characters.
func TruncateToTokens(text string, maxTokens int, tok Tokenizer) string {
	if tok.Count(text) <= maxTokens {
		return text
	}
	tokens := tok.Encode(text)
	if tokens != nil && len(tokens) > maxTokens {
		return tok.Decode(tokens[:maxTokens])
	}
	limit := maxTokens * 4
	if limit > len(text) {
		limit = len(text)
	}
	return text[:limit]
}

// MemoryTriggerDetector inspects a user message for memory write intent.
type MemoryTriggerDetector func(message string) (content string, detected bool)

// NewRuntime creates a Runtime over an Engine with sensible defaults.
//
// Memory writes are tool-driven: the LLM calls memory_create / memory_str_replace
// / memory_delete. There is no auto-write fallback — silent regex-based writers
// fight the taxonomy + MEMORY.md index discipline (they bypass dedup, types,
// and the read-before-write contract). Use WithMemoryTrigger to opt back in if
// you really want regex-based intent detection.
func NewRuntime(engine *Engine) *Runtime {
	return &Runtime{
		engine:         engine,
		maxSkills:      3,
		skillThreshold: 0.3,
		memoryTrigger:  nil,
		safety:         nil,
		tokenizer:      HeuristicTokenizer{},
	}
}

func (r *Runtime) WithMode(modeID string) *Runtime              { r.mode = modeID; return r }
func (r *Runtime) WithModel(m string) *Runtime                  { r.model = m; return r }
func (r *Runtime) WithSkillThreshold(t float64) *Runtime        { r.skillThreshold = t; return r }
func (r *Runtime) WithMaxSkills(n int) *Runtime                 { r.maxSkills = n; return r }
func (r *Runtime) WithMemoryTrigger(d MemoryTriggerDetector) *Runtime { r.memoryTrigger = d; return r }
func (r *Runtime) WithSafety(s SafetyFilter) *Runtime           { r.safety = s; return r }
func (r *Runtime) WithMemoryWriter(w *InferredMemoryWriter) *Runtime { r.memoryWriter = w; return r }
func (r *Runtime) WithTokenizer(t Tokenizer) *Runtime           { r.tokenizer = t; return r }
func (r *Runtime) WithConversationStore(s ConversationStore) *Runtime { r.store = s; return r }

// WithSessionContext attaches a provider that supplies SessionContext
// (time, location, user info, active artifact) on every turn.
// The context is rendered into LayerSession of the system prompt.
func (r *Runtime) WithSessionContext(p SessionContextProvider) *Runtime {
	r.sessionContext = p
	return r
}

// WithCompactor installs a Compactor that summarizes dropped conversation
// history when the context budget forces truncation. Without this, truncated
// messages are silently lost. With it, a summary is injected into LayerMemory.
func (r *Runtime) WithCompactor(c Compactor) *Runtime {
	r.compactor = c
	return r
}

// WithMaxMemoryTokens caps how many tokens of memory can be injected into
// LayerMemory. When the full memory exceeds this, the most recent/relevant
// entries are prioritized and the rest truncated.
// Default 0 = no cap (load everything).
func (r *Runtime) WithMaxMemoryTokens(n int) *Runtime {
	r.maxMemoryTokens = n
	return r
}

// WithMemoryRoots replaces the default memory roots with a custom set.
// Use this to read different directories per scope during orientation.
// Default: DefaultMemoryRoots (user/profile, user/facts, project/).
func (r *Runtime) WithMemoryRoots(roots ...MemoryRoot) *Runtime {
	r.memoryRootsV2 = roots
	return r
}

// WithMemoryEntrypoint enables loading a single index file (e.g. "MEMORY.md")
// from the root of each scope before the scoped roots. The entrypoint is
// truncated via TruncateEntrypoint (line + byte caps) and a freshness warning
// is appended when caps fire. Default "" = disabled.
//
// Pass EntrypointName for the canonical "MEMORY.md" name.
func (r *Runtime) WithMemoryEntrypoint(name string) *Runtime {
	r.entrypointName = name
	return r
}

// WithMemoryRecaller installs an LLM-side selector that filters the header
// manifest down to the files that are clearly relevant to the user's query.
// Without it the orientation surfaces the full manifest (descriptions only)
// so the LLM sees the inventory and can open files via memory_view itself.
func (r *Runtime) WithMemoryRecaller(rec MemoryRecaller) *Runtime {
	r.recaller = rec
	return r
}

// WithMaxRecalledMemories caps how many memory files the recall step
// surfaces in LayerMemory. Default 5 when a recaller is configured.
func (r *Runtime) WithMaxRecalledMemories(n int) *Runtime {
	r.recallMax = n
	return r
}

// WithThinkingBudget enables extended thinking for Claude 3.7+ models.
// budgetTokens is the maximum tokens the model can spend reasoning internally
// before producing a response. Minimum enforced by Anthropic API: 1024.
// Default 0 = disabled.
func (r *Runtime) WithThinkingBudget(budgetTokens int) *Runtime {
	r.thinkingBudget = budgetTokens
	return r
}

// WithMaxSkillTokens caps how many tokens of skill content can be injected
// into LayerSkills. When combined skill content exceeds this, it is truncated
// at token boundaries using the configured tokenizer.
//
// Default 0 = no cap.
func (r *Runtime) WithMaxSkillTokens(maxTokens int) *Runtime {
	if r.engine.HasPrompt() {
		r.engine.Prompt.SetMaxLayerTokens(LayerSkills, maxTokens)
	}
	return r
}

// Run executes a conversation turn. The Conversation accumulates state
// across calls — first call is "cold" (full orientation), subsequent calls
// are "warm" (reuse loaded skills and memory).
func (r *Runtime) Run(ctx context.Context, conv *Conversation, userMessage string) (*RuntimeResult, error) {
	if !r.engine.HasLLM() {
		return nil, fmt.Errorf("runtime: no LLM provider — set engine.LLM")
	}
	if conv == nil {
		return nil, fmt.Errorf("runtime: conversation is required")
	}

	tracer := TracerFromContext(ctx)
	if tracer == nil {
		tracer = NewTracer()
		ctx = WithTracer(ctx, tracer)
	}
	ctx, finishRun := StartSpan(ctx, "runtime.run", map[string]any{
		"conversation_id": conv.ID,
		"turn":            conv.TurnCount + 1,
	})
	rr := &RuntimeResult{
		StartedAt: time.Now(),
		TraceID:   string(tracer.TraceID()),
	}
	defer func() { finishRun(nil) }()

	conv.AppendUser(userMessage)

	// ── Orientation ──
	ctxOri, finishOri := StartSpan(ctx, "phase.orientation", map[string]any{
		"cold": conv.IsCold(),
	})
	var oriErr error
	if conv.IsCold() {
		oriErr = r.orientation(ctxOri, userMessage, conv, rr)
	} else {
		oriErr = r.warmRefresh(ctxOri, userMessage, conv)
	}
	finishOri(oriErr)
	if oriErr != nil {
		return rr, fmt.Errorf("orientation: %w", oriErr)
	}

	if err := ctx.Err(); err != nil {
		return rr, err
	}

	// ── Preparation ──
	ctxPrep, finishPrep := StartSpan(ctx, "phase.preparation", nil)
	if err := r.preparation(ctxPrep, conv, rr); err != nil {
		finishPrep(err)
		return rr, fmt.Errorf("preparation: %w", err)
	}
	finishPrep(nil)

	if err := ctx.Err(); err != nil {
		return rr, err
	}

	// ── Execution ──
	ctxExec, finishExec := StartSpan(ctx, "phase.execution", nil)
	loopResult, err := r.execution(ctxExec, conv, rr)
	finishExec(err)
	if err != nil {
		return rr, fmt.Errorf("execution: %w", err)
	}

	if loopResult != nil && loopResult.FinalContent != "" {
		conv.AppendAssistant(loopResult.FinalContent)
	}

	if err := ctx.Err(); err != nil {
		return rr, err
	}

	// ── Closure ──
	ctxClose, finishClose := StartSpan(ctx, "phase.closure", nil)
	if err := r.closure(ctxClose, userMessage, loopResult, conv, rr); err != nil {
		finishClose(err)
		return rr, fmt.Errorf("closure: %w", err)
	}
	finishClose(nil)

	conv.IncrementTurn()

	if r.store != nil {
		if err := r.store.Save(ctx, conv); err != nil {
			rr.Warnings = append(rr.Warnings, "save conversation: "+err.Error())
		}
	}

	rr.CompletedAt = time.Now()
	rr.Trace = tracer.Spans()
	return rr, nil
}

// ── Orientation (cold start only) ────────────────────────────────────────────

func (r *Runtime) orientation(ctx context.Context, userMessage string, conv *Conversation, rr *RuntimeResult) error {
	// Read memory → LayerMemory using labeled roots
	if r.engine.HasMemory() && r.engine.HasPrompt() {
		roots := r.memoryRootsV2
		if len(roots) == 0 {
			roots = DefaultMemoryRoots
		}
		var memContent strings.Builder

		// Entrypoint: load <scope>/<entrypointName> once per unique scope and
		// prepend it. The entrypoint is the always-loaded INDEX of memory —
		// pointers to topical files, not the topical files themselves.
		if r.entrypointName != "" {
			seenScopes := map[Scope]bool{}
			for _, root := range roots {
				if seenScopes[root.Scope] {
					continue
				}
				seenScopes[root.Scope] = true
				raw, err := r.engine.Memory.View(ctx, root.Scope, "/"+r.entrypointName)
				if err != nil || strings.TrimSpace(raw) == "" {
					continue
				}
				trunc := TruncateEntrypoint(raw)
				memContent.WriteString("## ")
				memContent.WriteString(string(root.Scope))
				memContent.WriteString(" memory index (`")
				memContent.WriteString(r.entrypointName)
				memContent.WriteString("`)\n\n")
				memContent.WriteString(trunc.Content)
				memContent.WriteString("\n\n")
			}
		}

		// Header manifest: per scope, scan all .md files (frontmatter only),
		// optionally filter via the recaller, and surface as a compact list so
		// the LLM knows what's available without loading bodies.
		if scanner, ok := r.engine.Memory.(MemoryHeaderScanner); ok {
			seen := map[Scope]bool{}
			for _, root := range roots {
				if seen[root.Scope] {
					continue
				}
				seen[root.Scope] = true
				headers, err := scanner.ScanHeaders(ctx, root.Scope, "/")
				if err != nil || len(headers) == 0 {
					continue
				}
				selected := headers
				if r.recaller != nil {
					maxN := r.recallMax
					if maxN <= 0 {
						maxN = 5
					}
					picked, err := r.recaller.Recall(ctx, RecallOptions{
						Query:      userMessage,
						Headers:    headers,
						MaxResults: maxN,
					})
					if err == nil && len(picked) > 0 {
						selected = picked
					}
				}
				memContent.WriteString("## ")
				memContent.WriteString(string(root.Scope))
				memContent.WriteString(" memory inventory\n\n")
				memContent.WriteString(FormatMemoryManifest(selected))
				memContent.WriteString("\n")
			}
		}
		if memContent.Len() > 0 {
			memStr := memContent.String()
			if r.maxMemoryTokens > 0 && r.tokenizer.Count(memStr) > r.maxMemoryTokens {
				memStr = evictMemoryToTokenBudget(memStr, r.maxMemoryTokens, r.tokenizer)
			}
			r.engine.Prompt.Set(LayerMemory, memStr)
			rr.MemoryRead = true
			conv.MemoryRead = true
		}
	}

	// Match and load skills → LayerSkills
	if err := r.matchAndLoadSkills(ctx, userMessage, conv, rr); err != nil {
		return err
	}

	// Build LayerSession: session context (time/location/user)
	r.buildSessionLayer(ctx, conv, userMessage)
	return nil
}

// ── Warm turn refresh ─────────────────────────────────────────────────────────

func (r *Runtime) warmRefresh(ctx context.Context, userMessage string, conv *Conversation) error {
	r.buildSessionLayer(ctx, conv, userMessage)

	if r.engine.HasSkills() {
		matches, err := r.engine.Skills.Match(ctx, userMessage)
		if err == nil {
			for _, m := range matches {
				if conv.IsSkillLoaded(m.Skill.Name) {
					continue
				}
				if m.Score < r.skillThreshold {
					break
				}
				skill, err := r.engine.Skills.Load(ctx, m.Skill.Name)
				if err != nil {
					continue
				}
				if r.engine.HasPrompt() {
					existing := r.engine.Prompt.Get(LayerSkills)
					newContent := existing
					if newContent != "" {
						newContent += "\n\n"
					}
					newContent += "# Skill: " + skill.Name + "\n\n" + skill.Content + "\n\n"
					cap := r.engine.Prompt.maxLayerTokens[LayerSkills]
					if cap > 0 && r.tokenizer.Count(newContent) > cap {
						newContent = TruncateToTokens(newContent, cap, r.tokenizer)
					}
					r.engine.Prompt.Set(LayerSkills, newContent)
				}
				conv.MarkSkillLoaded(skill.Name, m.Score, r.tokenizer.Count(skill.Content))
			}
		}
	}
	return nil
}

func (r *Runtime) matchAndLoadSkills(ctx context.Context, userMessage string, conv *Conversation, rr *RuntimeResult) error {
	if !r.engine.HasSkills() || !r.engine.HasPrompt() {
		return nil
	}
	matches, err := r.engine.Skills.Match(ctx, userMessage)
	if err != nil {
		return fmt.Errorf("skill match: %w", err)
	}
	var skillContent strings.Builder
	loaded := 0
	seen := make(map[string]bool)

	var loadSkill func(name string, depth int) *Skill
	loadSkill = func(name string, depth int) *Skill {
		if depth > 4 || seen[name] || conv.IsSkillLoaded(name) || loaded >= r.maxSkills {
			return nil
		}
		seen[name] = true
		skill, err := r.engine.Skills.Load(ctx, name)
		if err != nil {
			return nil
		}
		for _, dep := range skill.Meta.Requires {
			if dep != "" && dep != name {
				loadSkill(dep, depth+1)
			}
		}
		skillContent.WriteString("# Skill: ")
		skillContent.WriteString(skill.Name)
		skillContent.WriteString("\n\n")
		skillContent.WriteString(skill.Content)
		skillContent.WriteString("\n\n")
		conv.MarkSkillLoaded(skill.Name, 1.0, r.tokenizer.Count(skill.Content))
		rr.SkillsLoaded = append(rr.SkillsLoaded, skill.Name)
		loaded++
		return skill
	}

	for _, m := range matches {
		if loaded >= r.maxSkills {
			break
		}
		if m.Score < r.skillThreshold {
			break
		}
		loadSkill(m.Skill.Name, 0)
	}
	if skillContent.Len() > 0 {
		content := skillContent.String()
		if r.engine.HasPrompt() {
			cap := r.engine.Prompt.maxLayerTokens[LayerSkills]
			if cap > 0 && r.tokenizer.Count(content) > cap {
				content = TruncateToTokens(content, cap, r.tokenizer)
			}
		}
		r.engine.Prompt.Set(LayerSkills, content)
	}
	return nil
}

// buildSessionLayer assembles LayerSession from the SessionContextProvider.
func (r *Runtime) buildSessionLayer(ctx context.Context, conv *Conversation, userMessage string) {
	if !r.engine.HasPrompt() {
		return
	}
	if r.sessionContext == nil {
		r.engine.Prompt.Clear(LayerSession)
		return
	}
	sc, err := r.sessionContext.Get(ctx, conv)
	if err != nil || sc == nil {
		r.engine.Prompt.Clear(LayerSession)
		return
	}
	rendered := sc.Format()
	if rendered == "" {
		r.engine.Prompt.Clear(LayerSession)
		return
	}
	r.engine.Prompt.Set(LayerSession, rendered)
}

// ── Preparation ──────────────────────────────────────────────────────────────

func (r *Runtime) preparation(ctx context.Context, conv *Conversation, rr *RuntimeResult) error {
	if r.engine.HasBudget() && r.engine.HasPrompt() {
		skillTokens := 0
		for _, s := range conv.LoadedSkills {
			skillTokens += s.TokenEstimate
		}
		memoryTokens := r.tokenizer.Count(r.engine.Prompt.Get(LayerMemory))

		compResult := EnforceWithCompaction(
			ctx,
			r.engine.Budget,
			r.compactor,
			conv,
			r.engine.Skills,
			skillTokens,
			memoryTokens,
		)
		enforce := compResult.EnforcementResult
		if enforce.OverflowTokens > 0 {
			rr.Enforcement = enforce
			if len(enforce.EvictedSkills) > 0 {
				rr.Warnings = append(rr.Warnings,
					fmt.Sprintf("budget: evicted %d skills", len(enforce.EvictedSkills)))
			}
			if enforce.TruncatedHistory {
				rr.Warnings = append(rr.Warnings,
					fmt.Sprintf("budget: dropped %d messages", enforce.HistoryDropped))
			}
			if enforce.StillOverflow {
				rr.Warnings = append(rr.Warnings, "budget: still over after enforcement")
			}
		}
		if compResult.Summary != "" && r.engine.HasPrompt() {
			r.engine.Prompt.Append(LayerMemory, "\n\nContext summary (from earlier turns):\n"+compResult.Summary)
		}
	}
	return nil
}

// ── Execution ────────────────────────────────────────────────────────────────

func (r *Runtime) execution(ctx context.Context, conv *Conversation, rr *RuntimeResult) (*AgentLoopResult, error) {
	cfg := AgentLoopConfig{
		MaxTurns: 50,
		OnToolCall: func(call ToolCallEntry) bool {
			if r.safety != nil {
				v := r.safety.Inspect(ctx, call)
				return v.Decision != SafetyBlock
			}
			return true
		},
	}

	if r.thinkingBudget > 0 {
		thinkingBudget := r.thinkingBudget
		cfg.BuildRequest = func(systemPrompt string, messages []ChatMessage, tools *ToolRegistry) ChatRequest {
			innerCfg := AgentLoopConfig{
				SystemPrompt: systemPrompt,
				Tools:        tools,
			}
			req := defaultBuildRequest(innerCfg, messages)
			req.ThinkingBudget = thinkingBudget
			return req
		}
	}

	if r.engine.HasPrompt() {
		if r.mode != "" && r.engine.HasModes() {
			if mode, err := r.engine.Modes.Get(ctx, r.mode); err == nil {
				r.engine.Prompt.Set(LayerMode, mode.PromptContent)
				if cfg.Model == "" && mode.ModelSettings != nil {
					cfg.Model = mode.ModelSettings.Model
				}
			}
		}
		cfg.SystemPrompt = r.engine.Prompt.Build()
	}

	loopResult, err := RunAgentLoopWithEngine(ctx, r.engine, r.mode, cfg, conv.Messages)
	if err != nil {
		return nil, err
	}
	rr.Turns += loopResult.TotalTurns
	rr.Usage.PromptTokens += loopResult.TotalUsage.PromptTokens
	rr.Usage.CompletionTokens += loopResult.TotalUsage.CompletionTokens
	rr.Usage.TotalTokens += loopResult.TotalUsage.TotalTokens
	rr.StopReason = loopResult.StopReason
	rr.Response = loopResult.FinalContent
	return loopResult, nil
}

// ── Closure ──────────────────────────────────────────────────────────────────

func (r *Runtime) closure(ctx context.Context, userMessage string, loopResult *AgentLoopResult, conv *Conversation, rr *RuntimeResult) error {
	if r.engine.HasMemory() && r.memoryTrigger != nil {
		content, detected := r.memoryTrigger(userMessage)
		if detected && content != "" {
			r.handleMemoryTrigger(ctx, content, rr)
		}
	}

	if r.memoryWriter != nil && loopResult != nil && r.engine.HasMemory() {
		facts, err := r.memoryWriter.Extract(ctx, conv, loopResult.FinalContent)
		if err == nil && len(facts) > 0 {
			written, _ := r.memoryWriter.WriteWithDedup(ctx, r.engine.Memory, facts)
			for _, fact := range written {
				if fact.Path != "" {
					rr.MemoryWritten = append(rr.MemoryWritten, fact.Path)
				}
				rr.InferredFacts = append(rr.InferredFacts, fact)
			}
		}
	}

	return nil
}

// handleMemoryTrigger writes, replaces, or deletes a memory entry.
func (r *Runtime) handleMemoryTrigger(ctx context.Context, content string, rr *RuntimeResult) {
	if strings.HasPrefix(content, "FORGET: ") {
		query := strings.TrimPrefix(content, "FORGET: ")
		existing, _ := r.engine.Memory.Search(ctx, ScopeUser, query)
		for _, entry := range existing {
			if entry.Path == "" {
				continue
			}
			if stringSimilarity(entry.Content, query) >= 0.4 {
				if err := r.engine.Memory.Delete(ctx, ScopeUser, entry.Path); err == nil {
					rr.MemoryWritten = append(rr.MemoryWritten, "deleted:"+entry.Path)
				}
			}
		}
		return
	}

	// No fallback create here. The new model relies on the LLM invoking
	// memory_create / memory_str_replace as tool calls so type, frontmatter,
	// MEMORY.md indexing, and dedup all happen. Regex-based silent writes to
	// /facts/{nano}.md bypassed all of that and produced orphaned junk files.
}

// ── RuntimeResult ─────────────────────────────────────────────────────────────

type RuntimeResult struct {
	Response      string             `json:"response"`
	Turns         int                `json:"turns"`
	Usage         TokenUsage         `json:"usage"`
	StopReason    string             `json:"stop_reason"`
	Trace         []Span             `json:"trace,omitempty"`
	TraceID       string             `json:"trace_id,omitempty"`
	SkillsLoaded  []string           `json:"skills_loaded,omitempty"`
	MemoryRead    bool               `json:"memory_read"`
	MemoryWritten []string           `json:"memory_written,omitempty"`
	InferredFacts []InferredFact     `json:"inferred_facts,omitempty"`
	Warnings      []string           `json:"warnings,omitempty"`
	Enforcement   *EnforcementResult `json:"enforcement,omitempty"`
	StartedAt     time.Time          `json:"started_at"`
	CompletedAt   time.Time          `json:"completed_at"`
}

// ── Defaults ──────────────────────────────────────────────────────────────────

// DefaultMemoryTriggerDetector detects English + Spanish memory triggers.
func DefaultMemoryTriggerDetector(message string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(message))

	explicit := []string{
		"remember that ", "please remember ", "don't forget that ", "don't forget ",
		"update your memory ", "save that ",
		"recuerda que ", "por favor recuerda ", "no olvides que ", "no olvides ",
		"actualiza tu memoria ", "guarda que ",
	}
	for _, trigger := range explicit {
		if idx := strings.Index(lower, trigger); idx >= 0 {
			content := strings.TrimSpace(message[idx+len(trigger):])
			if content != "" {
				return content, true
			}
		}
	}

	stateChange := []string{
		"i no longer ", "i moved to ", "i now work at ", "i changed ", "i started ", "my name is ", "i am ",
		"ya no ", "me mudé a ", "ahora trabajo en ", "cambié ", "empecé ", "mi nombre es ", "me llamo ",
	}
	for _, trigger := range stateChange {
		if idx := strings.Index(lower, trigger); idx >= 0 {
			rest := message[idx:]
			end := strings.IndexAny(rest, ".\n")
			if end < 0 {
				end = len(rest)
			}
			content := strings.TrimSpace(rest[:end])
			if content != "" {
				return content, true
			}
		}
	}

	forget := []string{
		"forget about ", "please forget ",
		"olvida ", "olvídate de ",
	}
	for _, trigger := range forget {
		if idx := strings.Index(lower, trigger); idx >= 0 {
			content := strings.TrimSpace(message[idx+len(trigger):])
			if content != "" {
				return "FORGET: " + content, true
			}
		}
	}

	return "", false
}

// evictMemoryToTokenBudget truncates memory content to fit within maxTokens.
// Prefers keeping the most recent entries (bottom of file).
func evictMemoryToTokenBudget(content string, maxTokens int, tok Tokenizer) string {
	paragraphs := strings.Split(strings.TrimSpace(content), "\n\n")
	if len(paragraphs) == 0 {
		return content
	}

	var kept []string
	used := 0
	for i := len(paragraphs) - 1; i >= 0; i-- {
		p := strings.TrimSpace(paragraphs[i])
		if p == "" {
			continue
		}
		tokens := tok.Count(p)
		if used+tokens > maxTokens {
			break
		}
		kept = append([]string{p}, kept...)
		used += tokens
	}

	if len(kept) == 0 {
		last := strings.TrimSpace(paragraphs[len(paragraphs)-1])
		return last
	}
	if len(kept) < len(paragraphs) {
		kept = append([]string{"[Earlier memory entries omitted — token budget exceeded]"}, kept...)
	}
	return strings.Join(kept, "\n\n")
}
