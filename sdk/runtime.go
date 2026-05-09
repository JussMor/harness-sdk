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
	safety          SafetyFilter
	tokenizer       Tokenizer
	store           ConversationStore
	sessionContext  SessionContextProvider
	compactor       Compactor
	maxMemoryTokens int
	memoryRootsV2   []MemoryRoot
	thinkingBudget  int
	interruptGate   *InterruptGate
	permissions     *PermissionEngine
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

// NewRuntime creates a Runtime over an Engine with sensible defaults.
func NewRuntime(engine *Engine) *Runtime {
	return &Runtime{
		engine:    engine,
		safety:    nil,
		tokenizer: HeuristicTokenizer{},
	}
}

func (r *Runtime) WithMode(modeID string) *Runtime    { r.mode = modeID; return r }
func (r *Runtime) WithModel(m string) *Runtime        { r.model = m; return r }
func (r *Runtime) WithSafety(s SafetyFilter) *Runtime { r.safety = s; return r }

// WithPermissions installs a v3 PermissionEngine that runs before tool
// execution. It supersedes the per-tool CheckPermissions callback path:
// when an engine is set, the dispatcher hands every call through it. The
// engine itself folds the legacy CheckPermissions in as a fallback step,
// so existing tools keep working while gaining declarative rules + an
// optional human approver.
func (r *Runtime) WithPermissions(eng *PermissionEngine) *Runtime {
	r.permissions = eng
	return r
}
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

// WithThinkingBudget enables extended thinking for Claude 3.7+ models.
// budgetTokens is the maximum tokens the model can spend reasoning internally
// before producing a response. Minimum enforced by Anthropic API: 1024.
// Default 0 = disabled.
func (r *Runtime) WithThinkingBudget(budgetTokens int) *Runtime {
	r.thinkingBudget = budgetTokens
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
		for _, root := range roots {
			content, err := r.engine.Memory.View(ctx, root.Scope, root.Path)
			if err != nil || strings.TrimSpace(content) == "" {
				continue
			}
			if root.Label != "" {
				memContent.WriteString("## ")
				memContent.WriteString(root.Label)
				memContent.WriteString("\n\n")
			}
			memContent.WriteString(strings.TrimSpace(content))
			memContent.WriteString("\n\n")
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

	// Skills are loaded lazily by the Skill tool — no orientation-time matching.
	r.buildSessionLayer(ctx, conv, userMessage)
	return nil
}

// ── Warm turn refresh ─────────────────────────────────────────────────────────

// warmRefresh runs on non-cold turns. Only the session layer needs to be
// rebuilt; skills are loaded lazily through the Skill tool.
func (r *Runtime) warmRefresh(ctx context.Context, userMessage string, conv *Conversation) error {
	r.buildSessionLayer(ctx, conv, userMessage)
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
		memoryTokens := r.tokenizer.Count(r.engine.Prompt.Get(LayerMemory))

		compResult := EnforceWithCompaction(
			ctx,
			r.engine.Budget,
			r.compactor,
			conv,
			memoryTokens,
		)
		enforce := compResult.EnforcementResult
		if enforce.OverflowTokens > 0 {
			rr.Enforcement = enforce
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
		MaxTurns:    50,
		Permissions: r.permissions,
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

	// Inject per-turn dynamic reminders (skills listing, agents listing, …)
	// as <system-reminder> blocks appended to the system prompt. Without this
	// the LLM is unaware of the lazy-loaded skills/agents catalog and tells
	// the user it has none.
	if r.engine.HasTools() {
		if blocks, err := r.engine.Tools.CollectDynamicReminders(ctx); err == nil && len(blocks) > 0 {
			reminder := SystemReminder(JoinSystemReminders(blocks...))
			if reminder != "" {
				if cfg.SystemPrompt != "" {
					cfg.SystemPrompt += "\n\n" + reminder
				} else {
					cfg.SystemPrompt = reminder
				}
			}
		}
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
	// In v3 all memory writes go through the Memory tool driven by the LLM,
	// with read-before-write validation in the tool layer. The runtime no
	// longer inspects user messages for trigger phrases.
	_ = ctx
	_ = userMessage
	_ = loopResult
	_ = conv
	_ = rr
	return nil
}

// ── RuntimeResult ─────────────────────────────────────────────────────────────

type RuntimeResult struct {
	Response      string             `json:"response"`
	Turns         int                `json:"turns"`
	Usage         TokenUsage         `json:"usage"`
	StopReason    string             `json:"stop_reason"`
	Trace         []Span             `json:"trace,omitempty"`
	TraceID       string             `json:"trace_id,omitempty"`
	MemoryRead    bool               `json:"memory_read"`
	MemoryWritten []string           `json:"memory_written,omitempty"`
	Warnings      []string           `json:"warnings,omitempty"`
	Enforcement   *EnforcementResult `json:"enforcement,omitempty"`
	StartedAt     time.Time          `json:"started_at"`
	CompletedAt   time.Time          `json:"completed_at"`
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
