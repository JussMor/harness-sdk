package autobuild

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Runtime is the orchestrator that connects every provider in an Engine
// into a working agent that operates like Claude.
//
// Without Runtime, an Engine is a bag of unconnected providers — the
// consumer must manually:
//   - Read memory at conversation start and inject into the prompt
//   - Match skills against the user message and load them
//   - Verify context budget before each LLM call (with enforcement, not warnings)
//   - Surface relevant observations into the prompt
//   - Detect memory write triggers in user messages
//   - Filter tool results into observations
//   - Run safety checks before tool dispatch
//   - Verify output before closure
//   - Persist conversation state across process restarts
//
// Runtime does all of this automatically. The consumer provides messages
// via a Conversation and gets back results.
//
// Cold vs warm turns:
//
//   First Run on a Conversation triggers full orientation (memory read,
//   skill matching, observation surface). Subsequent Runs on the same
//   Conversation skip orientation and reuse loaded state — this matches
//   how Claude operates within a single conversation.
type Runtime struct {
	engine *Engine
	mode   string

	// Configurable behavior
	maxSkills       int
	skillThreshold  float64
	autoCheckpoint  bool
	memoryRoots     []string
	maxVerifyRetry  int
	observationFilt ObservationFilter
	memoryTrigger   MemoryTriggerDetector
	wellbeing       WellbeingDetector
	verification    VerificationStrategy
	safety          SafetyFilter
	memoryWriter    *InferredMemoryWriter
	evictionPolicy  SkillEvictionPolicy
	tokenizer       Tokenizer
	store           ConversationStore
}

// Tokenizer estimates token count for a string. Replace the default heuristic
// with a real tokenizer (tiktoken, claude-tokenizer) for accurate budgets.
type Tokenizer interface {
	Count(text string) int
}

// HeuristicTokenizer is the default — chars/4. Good enough for English.
// Inaccurate for languages with longer tokens (Spanish, German) or shorter
// (Chinese, Japanese). Replace for production.
type HeuristicTokenizer struct{}

func (HeuristicTokenizer) Count(text string) int { return len(text) / 4 }

// ObservationFilter decides if a tool result should become an Observation.
type ObservationFilter func(call ToolCallEntry, result ToolResult) Observation

// MemoryTriggerDetector inspects a user message for memory write intent.
type MemoryTriggerDetector func(message string) (layer MemoryLayer, content string, detected bool)

// NewRuntime creates a Runtime over an Engine with sensible defaults.
func NewRuntime(engine *Engine) *Runtime {
	return &Runtime{
		engine:          engine,
		maxSkills:       3,
		skillThreshold:  0.3,
		autoCheckpoint:  true,
		memoryRoots:     []string{"/"},
		maxVerifyRetry:  2,
		observationFilt: DefaultObservationFilter,
		memoryTrigger:   DefaultMemoryTriggerDetector,
		wellbeing:       DefaultWellbeingDetector{},
		verification:    NoOpVerification{},
		safety:          nil,
		evictionPolicy:  LRUEvictionPolicy{},
		tokenizer:       HeuristicTokenizer{},
	}
}

func (r *Runtime) WithMode(modeID string) *Runtime              { r.mode = modeID; return r }
func (r *Runtime) WithSkillThreshold(t float64) *Runtime        { r.skillThreshold = t; return r }
func (r *Runtime) WithMaxSkills(n int) *Runtime                 { r.maxSkills = n; return r }
func (r *Runtime) WithObservationFilter(f ObservationFilter) *Runtime { r.observationFilt = f; return r }
func (r *Runtime) WithMemoryTrigger(d MemoryTriggerDetector) *Runtime { r.memoryTrigger = d; return r }
func (r *Runtime) WithWellbeing(d WellbeingDetector) *Runtime   { r.wellbeing = d; return r }
func (r *Runtime) WithVerification(v VerificationStrategy) *Runtime { r.verification = v; return r }
func (r *Runtime) WithSafety(s SafetyFilter) *Runtime           { r.safety = s; return r }
func (r *Runtime) WithMemoryWriter(w *InferredMemoryWriter) *Runtime { r.memoryWriter = w; return r }
func (r *Runtime) WithTokenizer(t Tokenizer) *Runtime           { r.tokenizer = t; return r }
func (r *Runtime) WithConversationStore(s ConversationStore) *Runtime { r.store = s; return r }
func (r *Runtime) WithMaxVerifyRetry(n int) *Runtime            { r.maxVerifyRetry = n; return r }

// Run executes a conversation turn. The Conversation accumulates state
// across calls — first call is "cold" (full orientation), subsequent calls
// are "warm" (reuse loaded skills and memory).
//
// Cancellation propagates through ctx into every phase. If ctx is cancelled
// mid-phase, Runtime returns immediately without proceeding.
func (r *Runtime) Run(ctx context.Context, conv *Conversation, userMessage string) (*RuntimeResult, error) {
	if !r.engine.HasLLM() {
		return nil, fmt.Errorf("runtime: no LLM provider — set engine.LLM")
	}
	if conv == nil {
		return nil, fmt.Errorf("runtime: conversation is required")
	}

	// Tracing
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

	// Wellbeing pre-check on user message
	if r.wellbeing != nil {
		signal := r.wellbeing.Detect(userMessage)
		if signal.Detected {
			rr.WellbeingSignal = &signal
			// High severity: short-circuit. Don't dispatch tools, surface support.
			if signal.Severity >= WellbeingSeverityHigh {
				rr.Response = wellbeingResponse(signal)
				rr.StopReason = "wellbeing_intercept"
				rr.CompletedAt = time.Now()
				return rr, nil
			}
		}
	}

	// Append user message to conversation
	conv.AppendUser(userMessage)

	// ── Cold start: full orientation ──
	if conv.IsCold() {
		ctxOri, finishOri := StartSpan(ctx, "phase.orientation", nil)
		if err := r.orientation(ctxOri, userMessage, conv, rr); err != nil {
			finishOri(err)
			return rr, fmt.Errorf("orientation: %w", err)
		}
		finishOri(nil)
	} else {
		// Warm turn: only refresh observations and re-match skills if message differs significantly
		ctxWarm, finishWarm := StartSpan(ctx, "phase.warm_refresh", nil)
		if err := r.warmRefresh(ctxWarm, userMessage, conv); err != nil {
			finishWarm(err)
			return rr, fmt.Errorf("warm refresh: %w", err)
		}
		finishWarm(nil)
	}

	if err := ctx.Err(); err != nil {
		return rr, err
	}

	// ── Preparation: checkpoint + budget enforcement ──
	ctxPrep, finishPrep := StartSpan(ctx, "phase.preparation", nil)
	if err := r.preparation(ctxPrep, conv, rr); err != nil {
		finishPrep(err)
		return rr, fmt.Errorf("preparation: %w", err)
	}
	finishPrep(nil)

	if err := ctx.Err(); err != nil {
		return rr, err
	}

	// ── Execution + Verification with retry loop ──
	var loopResult *AgentLoopResult
	for attempt := 0; attempt <= r.maxVerifyRetry; attempt++ {
		ctxExec, finishExec := StartSpan(ctx, "phase.execution", map[string]any{"attempt": attempt + 1})
		var err error
		loopResult, err = r.execution(ctxExec, conv, rr)
		finishExec(err)
		if err != nil {
			return rr, fmt.Errorf("execution: %w", err)
		}

		// Verification
		ctxVer, finishVer := StartSpan(ctx, "phase.verification", nil)
		verdict := r.verification.Verify(ctxVer, loopResult, conv)
		finishVer(nil)
		rr.VerificationVerdict = &verdict

		if verdict.Pass {
			break
		}
		if !verdict.Retry || attempt >= r.maxVerifyRetry {
			rr.Warnings = append(rr.Warnings, "verification failed: "+verdict.Reason)
			break
		}
		// Push retry message back into conversation
		conv.AppendUser(fmt.Sprintf("Verification failed: %s. Please address and retry.", verdict.Reason))
	}

	// Append final response to conversation
	if loopResult != nil && loopResult.FinalContent != "" {
		conv.AppendAssistant(loopResult.FinalContent)
	}

	if err := ctx.Err(); err != nil {
		return rr, err
	}

	// ── Closure: memory writes ──
	ctxClose, finishClose := StartSpan(ctx, "phase.closure", nil)
	if err := r.closure(ctxClose, userMessage, loopResult, conv, rr); err != nil {
		finishClose(err)
		return rr, fmt.Errorf("closure: %w", err)
	}
	finishClose(nil)

	conv.IncrementTurn()

	// Persist conversation
	if r.store != nil {
		if err := r.store.Save(ctx, conv); err != nil {
			rr.Warnings = append(rr.Warnings, "save conversation: "+err.Error())
		}
	}

	rr.CompletedAt = time.Now()
	rr.Trace = tracer.Spans()
	return rr, nil
}

// ── Phase 0: Orientation (cold start only) ───────────────────────────────────

func (r *Runtime) orientation(ctx context.Context, userMessage string, conv *Conversation, rr *RuntimeResult) error {
	if r.engine.HasPrompt() {
		if !r.engine.Prompt.Has(LayerBehavior) {
			r.engine.Prompt.Set(LayerBehavior, DefaultBehaviorPrompt)
		}
	}

	// Read memory → LayerMemory
	if r.engine.HasMemory() && r.engine.HasPrompt() {
		var memContent strings.Builder
		for _, root := range r.memoryRoots {
			if content, err := r.engine.Memory.View(ctx, ScopeUser, root); err == nil && content != "" {
				memContent.WriteString(content)
				memContent.WriteString("\n\n")
			}
			if content, err := r.engine.Memory.View(ctx, ScopeProject, root); err == nil && content != "" {
				memContent.WriteString(content)
				memContent.WriteString("\n\n")
			}
		}
		if memContent.Len() > 0 {
			r.engine.Prompt.Set(LayerMemory, memContent.String())
			rr.MemoryRead = true
			conv.MemoryRead = true
		}
	}

	// Match and load skills → LayerSkills
	if err := r.matchAndLoadSkills(ctx, userMessage, conv, rr); err != nil {
		return err
	}

	// Surface observations → LayerSession
	r.surfaceObservations(ctx, userMessage)
	return nil
}

// ── Warm turn refresh ────────────────────────────────────────────────────────

func (r *Runtime) warmRefresh(ctx context.Context, userMessage string, conv *Conversation) error {
	// On warm turns we only:
	//   1. Re-surface observations (cheap, relevant per-turn)
	//   2. Match for new skills not already loaded
	r.surfaceObservations(ctx, userMessage)

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
					r.engine.Prompt.Append(LayerSkills, "# Skill: "+skill.Name+"\n\n"+skill.Content+"\n\n")
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
	for _, m := range matches {
		if loaded >= r.maxSkills {
			break
		}
		if m.Score < r.skillThreshold {
			break
		}
		skill, err := r.engine.Skills.Load(ctx, m.Skill.Name)
		if err != nil {
			continue
		}
		skillContent.WriteString("# Skill: ")
		skillContent.WriteString(skill.Name)
		skillContent.WriteString("\n\n")
		skillContent.WriteString(skill.Content)
		skillContent.WriteString("\n\n")
		conv.MarkSkillLoaded(skill.Name, m.Score, r.tokenizer.Count(skill.Content))
		rr.SkillsLoaded = append(rr.SkillsLoaded, skill.Name)
		loaded++
	}
	if skillContent.Len() > 0 {
		r.engine.Prompt.Set(LayerSkills, skillContent.String())
	}
	return nil
}

func (r *Runtime) surfaceObservations(ctx context.Context, userMessage string) {
	if !r.engine.HasObservations() || !r.engine.HasPrompt() {
		return
	}
	obs, err := r.engine.Observations.Relevant(ctx, userMessage, 5)
	if err != nil || len(obs) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("Recent observations:\n")
	for _, o := range obs {
		b.WriteString("- [")
		b.WriteString(o.Source)
		b.WriteString("] ")
		b.WriteString(o.Content)
		b.WriteString("\n")
	}
	r.engine.Prompt.Set(LayerSession, b.String())
}

// ── Phase 2: Preparation ─────────────────────────────────────────────────────

func (r *Runtime) preparation(ctx context.Context, conv *Conversation, rr *RuntimeResult) error {
	if r.autoCheckpoint && r.engine.HasCheckpoints() {
		if cp, err := r.engine.Checkpoints.Create(ctx, "Pre-execution checkpoint"); err == nil {
			rr.CheckpointID = cp.ID
		}
	}

	// Real budget enforcement (not just warnings)
	if r.engine.HasBudget() && r.engine.HasPrompt() {
		assembled := r.engine.Prompt.Build()
		skillTokens := 0
		for _, s := range conv.LoadedSkills {
			skillTokens += s.TokenEstimate
		}
		memoryTokens := r.tokenizer.Count(r.engine.Prompt.Get(LayerMemory))
		_ = assembled

		enforce := r.engine.Budget.Enforce(
			ctx, conv, r.engine.Skills,
			skillTokens, memoryTokens,
			&conv.Messages,
		)
		if enforce.OverflowTokens > 0 {
			rr.Enforcement = enforce
			if len(enforce.EvictedSkills) > 0 {
				rr.Warnings = append(rr.Warnings,
					fmt.Sprintf("budget enforcement: evicted %d skills", len(enforce.EvictedSkills)))
			}
			if enforce.TruncatedHistory {
				rr.Warnings = append(rr.Warnings,
					fmt.Sprintf("budget enforcement: dropped %d history messages", enforce.HistoryDropped))
			}
			if enforce.StillOverflow {
				rr.Warnings = append(rr.Warnings, "budget enforcement: still over budget after eviction")
			}
		}
	}
	return nil
}

// ── Phase 3: Execution ───────────────────────────────────────────────────────

func (r *Runtime) execution(ctx context.Context, conv *Conversation, rr *RuntimeResult) (*AgentLoopResult, error) {
	cfg := AgentLoopConfig{
		MaxTurns: 50,
		OnToolCall: func(call ToolCallEntry) bool {
			// Safety filter
			if r.safety != nil {
				v := r.safety.Inspect(ctx, call)
				return v.Decision != SafetyBlock
			}
			return true
		},
		OnToolResult: func(call ToolCallEntry, result ToolResult) ToolResult {
			if r.engine.HasObservations() && r.observationFilt != nil {
				obs := r.observationFilt(call, result)
				if obs.Content != "" {
					_ = r.engine.Observations.Record(ctx, obs)
				}
			}
			return result
		},
	}

	if r.engine.HasPrompt() {
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

// ── Phase 5: Closure ─────────────────────────────────────────────────────────

func (r *Runtime) closure(ctx context.Context, userMessage string, loopResult *AgentLoopResult, conv *Conversation, rr *RuntimeResult) error {
	// Explicit memory triggers
	if r.engine.HasMemory() && r.memoryTrigger != nil {
		layer, content, detected := r.memoryTrigger(userMessage)
		if detected && content != "" {
			if layer == MemoryLayerSession && r.engine.HasObservations() {
				_ = r.engine.Observations.Record(ctx, Observation{
					Source:    "user_message",
					Content:   content,
					Relevance: 0.7,
					CreatedAt: time.Now(),
				})
			} else {
				path := fmt.Sprintf("/facts/%d.md", time.Now().UnixNano())
				if err := r.engine.Memory.Create(ctx, ScopeUser, path, content); err == nil {
					rr.MemoryWritten = append(rr.MemoryWritten, path)
				}
			}
		}
	}

	// Inferred memory writes (LLM identifies persistent facts)
	if r.memoryWriter != nil && loopResult != nil && r.engine.HasMemory() {
		facts, err := r.memoryWriter.Extract(ctx, conv, loopResult.FinalContent)
		if err == nil {
			for _, fact := range facts {
				path := fmt.Sprintf("/facts/inferred-%d.md", time.Now().UnixNano())
				if err := r.engine.Memory.Create(ctx, fact.Scope, path, fact.Content); err == nil {
					rr.MemoryWritten = append(rr.MemoryWritten, path)
					rr.InferredFacts = append(rr.InferredFacts, fact)
				}
			}
		}
	}
	return nil
}

// ── RuntimeResult ────────────────────────────────────────────────────────────

type RuntimeResult struct {
	Response          string             `json:"response"`
	Turns             int                `json:"turns"`
	Usage             TokenUsage         `json:"usage"`
	StopReason        string             `json:"stop_reason"`
	Trace             []Span             `json:"trace,omitempty"`
	TraceID           string             `json:"trace_id,omitempty"`
	SkillsLoaded      []string           `json:"skills_loaded,omitempty"`
	MemoryRead        bool               `json:"memory_read"`
	MemoryWritten     []string           `json:"memory_written,omitempty"`
	InferredFacts     []InferredFact     `json:"inferred_facts,omitempty"`
	CheckpointID      string             `json:"checkpoint_id,omitempty"`
	Warnings          []string           `json:"warnings,omitempty"`
	WellbeingSignal   *WellbeingSignal   `json:"wellbeing_signal,omitempty"`
	VerificationVerdict *Verdict         `json:"verification_verdict,omitempty"`
	Enforcement       *EnforcementResult `json:"enforcement,omitempty"`
	StartedAt         time.Time          `json:"started_at"`
	CompletedAt       time.Time          `json:"completed_at"`
}

// ── Defaults ─────────────────────────────────────────────────────────────────

// DefaultObservationFilter records tool results that look reusable.
func DefaultObservationFilter(call ToolCallEntry, result ToolResult) Observation {
	if result.Error != nil {
		return Observation{}
	}
	content := strings.TrimSpace(result.Content)
	const minContentLength = 50    // skip trivial confirmations
	const maxContentLength = 2000  // cap to prevent dumping huge results
	if len(content) < minContentLength {
		return Observation{}
	}
	if len(content) > maxContentLength {
		content = content[:maxContentLength] + "..."
	}
	return Observation{
		Source:    call.Name,
		Content:   content,
		Relevance: 0.6,
		CreatedAt: time.Now(),
	}
}

// DefaultMemoryTriggerDetector detects English + Spanish memory triggers.
func DefaultMemoryTriggerDetector(message string) (MemoryLayer, string, bool) {
	lower := strings.ToLower(strings.TrimSpace(message))

	// Explicit triggers — multilingual
	explicit := []string{
		// English
		"remember that ", "please remember ", "don't forget that ", "don't forget ",
		"update your memory ", "save that ",
		// Spanish
		"recuerda que ", "por favor recuerda ", "no olvides que ", "no olvides ",
		"actualiza tu memoria ", "guarda que ",
	}
	for _, trigger := range explicit {
		if idx := strings.Index(lower, trigger); idx >= 0 {
			content := strings.TrimSpace(message[idx+len(trigger):])
			if content != "" {
				return MemoryLayerExplicit, content, true
			}
		}
	}

	// State change — multilingual
	stateChange := []string{
		// English
		"i no longer ", "i moved to ", "i now work at ", "i changed ", "i started ",
		// Spanish
		"ya no ", "me mudé a ", "ahora trabajo en ", "cambié ", "empecé ",
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
				return MemoryLayerExplicit, content, true
			}
		}
	}

	// Forget triggers
	forget := []string{
		"forget about ", "please forget ",
		"olvida ", "olvídate de ",
	}
	for _, trigger := range forget {
		if idx := strings.Index(lower, trigger); idx >= 0 {
			content := strings.TrimSpace(message[idx+len(trigger):])
			if content != "" {
				return MemoryLayerInferred, "FORGET: " + content, true
			}
		}
	}

	return "", "", false
}

// EstimateTokens is the legacy heuristic — kept for backward compat.
// Prefer Tokenizer.Count via Runtime.WithTokenizer.
func EstimateTokens(text string) int {
	return HeuristicTokenizer{}.Count(text)
}

func wellbeingResponse(s WellbeingSignal) string {
	switch s.Category {
	case WellbeingCategorySelfHarm:
		return "I'm concerned about what you've shared. If you're in immediate danger, please contact a crisis line: in the US, 988 (call or text). For other regions, see https://findahelpline.com — they have local resources. I'm here to listen if you want to talk about what's going on, but please reach out to someone who can be physically present with you too."
	case WellbeingCategoryEatingDisorder:
		return "What you're describing sounds like it might be hurting you. The National Alliance for Eating Disorders helpline (1-866-662-1235) has people you can talk to. I'm not the right tool for this — a person who can actually be there will help more than I can."
	default:
		return "It sounds like you're going through something hard. I'm happy to keep talking, but I want you to know that talking to someone you trust — a friend, family member, or professional — can help in ways I can't."
	}
}
