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
	maxVerifyRetry  int
	observationFilt ObservationFilter
	memoryTrigger   MemoryTriggerDetector
	wellbeing       WellbeingDetector
	verification    VerificationStrategy
	safety          SafetyFilter
	memoryWriter    *InferredMemoryWriter
	tokenizer       Tokenizer
	store           ConversationStore
	planner         Planner
	autoApprovePlan bool
	sessionContext  SessionContextProvider
	outputFilter    OutputFilter
	compactor       Compactor
	maxMemoryTokens int          // 0 = unlimited
	memoryRootsV2   []MemoryRoot // nil = use DefaultMemoryRoots
	thinkingBudget  int            // 0 = disabled; >0 enables extended thinking
	interruptGate   *InterruptGate // non-nil when human-in-the-loop / interrupts are active
	webhooks        *WebhookDispatcher // non-nil when outbound webhooks are configured
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

func (HeuristicTokenizer) Count(text string) int { return len(text) / 4 }
func (HeuristicTokenizer) Encode(text string) []int { return nil } // not supported
func (HeuristicTokenizer) Decode(tokens []int) string { return "" } // not supported

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
	// Fallback: character truncation
	limit := maxTokens * 4
	if limit > len(text) {
		limit = len(text)
	}
	return text[:limit]
}

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
		maxVerifyRetry:  2,
		observationFilt: DefaultObservationFilter,
		memoryTrigger:   DefaultMemoryTriggerDetector,
		wellbeing:       DefaultWellbeingDetector{},
		verification:    NoOpVerification{},
		safety:          nil,
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

// WithPlanner enables Alignment-phase planning. When set, Runtime asks
// the planner whether the turn warrants a structured Plan and, if so,
// proposes one on engine.Execution before Execution.
func (r *Runtime) WithPlanner(p Planner) *Runtime { r.planner = p; return r }

// WithAutoApprovePlan auto-approves any plan proposed during Alignment.
// Default false: plans are proposed but require explicit Approve to run.
func (r *Runtime) WithAutoApprovePlan(b bool) *Runtime { r.autoApprovePlan = b; return r }

// WithSessionContext attaches a provider that supplies SessionContext
// (time, location, user info, active artifact) on every turn.
// The context is rendered into LayerSession of the system prompt.
func (r *Runtime) WithSessionContext(p SessionContextProvider) *Runtime {
	r.sessionContext = p
	return r
}

// WithOutputFilter installs a filter that inspects the LLM's final response
// before returning it to the caller. Symmetric counterpart of WithSafety
// (which inspects tool calls). Use to redact secrets, add disclaimers,
// or block responses that violate output policy.
func (r *Runtime) WithOutputFilter(f OutputFilter) *Runtime {
	r.outputFilter = f
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
//
// Example — add a project-specific knowledge base:
//
//	runtime.WithMemoryRoots(autobuild.DefaultMemoryRoots..., autobuild.MemoryRoot{
//	    Scope: autobuild.ScopeProject, Path: "/knowledge", Label: "Domain knowledge",
//	})
func (r *Runtime) WithMemoryRoots(roots ...MemoryRoot) *Runtime {
	r.memoryRootsV2 = roots
	return r
}

// WithThinkingBudget enables extended thinking for Claude 3.7+ models.
// budgetTokens is the maximum tokens the model can spend reasoning internally
// before producing a response. Minimum enforced by Anthropic API: 1024.
// MaxTokens in each request is automatically increased when needed.
// Default 0 = disabled.
func (r *Runtime) WithThinkingBudget(budgetTokens int) *Runtime {
	r.thinkingBudget = budgetTokens
	return r
}

// WithMaxSkillTokens caps how many tokens of skill content can be injected
// into LayerSkills. When combined skill content exceeds this, it is truncated
// at token boundaries using the configured tokenizer.
//
// Without this cap, loading many skills simultaneously can overflow the system
// prompt context window. Recommended: 4000–8000 tokens.
// Default 0 = no cap.
func (r *Runtime) WithMaxSkillTokens(maxTokens int) *Runtime {
	if r.engine.HasPrompt() {
		r.engine.Prompt.SetMaxLayerTokens(LayerSkills, maxTokens)
	}
	return r
}

// WithWebhooks attaches a WebhookDispatcher to the runtime. Stream events
// emitted by RunStream are forwarded to all matching subscriptions in the
// store, signed with HMAC-SHA256.
//
// The returned dispatcher is owned by the runtime; call dispatcher.Close()
// when shutting down the process to drain in-flight deliveries.
//
// Usage:
//
//	store := ab.NewInMemoryWebhookStore()
//	dispatcher, runtime := ab.NewRuntime(engine).WithWebhooks(store)
//	defer dispatcher.Close()
func (r *Runtime) WithWebhooks(store WebhookStore) (*WebhookDispatcher, *Runtime) {
	d := NewWebhookDispatcher(store, nil)
	r.webhooks = d
	return d, r
}

// Webhooks returns the dispatcher attached to the runtime, or nil if
// outbound webhooks are not configured.
func (r *Runtime) Webhooks() *WebhookDispatcher { return r.webhooks }

// Run executes a conversation turn. The Conversation accumulates state
// across calls — first call is "cold" (full orientation), subsequent calls
// are "warm" (reuse loaded skills and memory).
//
// Cancellation propagates through ctx into every phase. If ctx is cancelled
// mid-phase, Runtime returns immediately without proceeding.
//
// Phase ownership: this Run drives engine.Execution as the single source
// of truth for phase state. Hooks registered via engine.Execution.RegisterHook
// fire on transitions. The tracing spans mirror the phase advance.
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

	// Always append user message first — even on wellbeing intercept,
	// the turn must be recorded for safety/audit reasons.
	conv.AppendUser(userMessage)

	// Wellbeing pre-check on user message
	if r.wellbeing != nil {
		signal := r.wellbeing.Detect(userMessage)
		if signal.Detected {
			rr.WellbeingSignal = &signal
			if signal.Severity >= WellbeingSeverityHigh {
				// High severity: short-circuit, but PERSIST the turn.
				// Skipping persistence here would silently lose the user's
				// most critical message — exactly the wrong move.
				rr.Response = wellbeingResponse(signal)
				rr.StopReason = "wellbeing_intercept"
				conv.AppendAssistant(rr.Response)
				conv.IncrementTurn()
				if r.store != nil {
					_ = r.store.Save(ctx, conv)
				}
				rr.CompletedAt = time.Now()
				rr.Trace = tracer.Spans()
				return rr, nil
			}
		}
	}

	// Reset ExecutionContext to Orientation for this turn.
	// Each Run starts fresh through the lifecycle.
	if r.engine.HasExecution() {
		_ = r.engine.Execution.SetPhase(ctx, PhaseOrientation)
	}

	// ── Phase 0: Orientation ──
	// Cold: full read of memory + skills + observations.
	// Warm: just refresh observations and look for new skill matches.
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

	// ── Phase 1: Alignment ──
	// Decide if this task warrants a plan. Propose, register on
	// ExecutionContext, optionally auto-approve.
	if err := r.advancePhase(ctx, PhaseAlignment); err != nil {
		return rr, fmt.Errorf("advance to alignment: %w", err)
	}
	ctxAlign, finishAlign := StartSpan(ctx, "phase.alignment", nil)
	if err := r.alignment(ctxAlign, userMessage, conv, rr); err != nil {
		finishAlign(err)
		return rr, fmt.Errorf("alignment: %w", err)
	}
	finishAlign(nil)

	if err := ctx.Err(); err != nil {
		return rr, err
	}

	// ── Phase 2: Preparation ──
	if err := r.advancePhase(ctx, PhasePreparation); err != nil {
		return rr, fmt.Errorf("advance to preparation: %w", err)
	}
	ctxPrep, finishPrep := StartSpan(ctx, "phase.preparation", nil)
	if err := r.preparation(ctxPrep, conv, rr); err != nil {
		finishPrep(err)
		return rr, fmt.Errorf("preparation: %w", err)
	}
	finishPrep(nil)

	if err := ctx.Err(); err != nil {
		return rr, err
	}

	// ── Phase 3+4: Execution + Verification with retry loop ──
	if err := r.advancePhase(ctx, PhaseExecution); err != nil {
		return rr, fmt.Errorf("advance to execution: %w", err)
	}
	var loopResult *AgentLoopResult
	for attempt := 0; attempt <= r.maxVerifyRetry; attempt++ {
		ctxExec, finishExec := StartSpan(ctx, "phase.execution", map[string]any{"attempt": attempt + 1})
		var err error
		loopResult, err = r.execution(ctxExec, conv, rr)
		finishExec(err)
		if err != nil {
			return rr, fmt.Errorf("execution: %w", err)
		}

		// Verification phase
		if err := r.advancePhase(ctx, PhaseVerification); err != nil {
			return rr, fmt.Errorf("advance to verification: %w", err)
		}
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
		// Retry: push verdict back as a user message and re-enter Execution.
		conv.AppendUser(fmt.Sprintf("Verification failed: %s. Please address and retry.", verdict.Reason))
		_ = r.engine.Execution
		if r.engine.HasExecution() {
			_ = r.engine.Execution.SetPhase(ctx, PhaseExecution)
		}
	}

	// Append final response to conversation
	if loopResult != nil && loopResult.FinalContent != "" {
		conv.AppendAssistant(loopResult.FinalContent)
	}

	if err := ctx.Err(); err != nil {
		return rr, err
	}

	// ── Phase 5: Closure ──
	if err := r.advancePhase(ctx, PhaseClosure); err != nil {
		return rr, fmt.Errorf("advance to closure: %w", err)
	}
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

// advancePhase moves engine.Execution to the target phase, firing any
// registered hooks. No-op if Execution is not wired.
func (r *Runtime) advancePhase(ctx context.Context, target Phase) error {
	if !r.engine.HasExecution() {
		return nil
	}
	return r.engine.Execution.SetPhase(ctx, target)
}

// ── Phase 1: Alignment ───────────────────────────────────────────────────────

// alignment decides whether this turn needs a structured plan. If yes,
// the Planner proposes one and (optionally) auto-approves. The Plan lives
// on engine.Execution and can be inspected via engine.Execution.ActivePlan().
func (r *Runtime) alignment(ctx context.Context, userMessage string, conv *Conversation, rr *RuntimeResult) error {
	if r.planner == nil || !r.engine.HasExecution() {
		return nil
	}
	if !r.planner.ShouldPlan(ctx, userMessage, conv) {
		return nil
	}
	plan, err := r.planner.Propose(ctx, userMessage, conv)
	if err != nil {
		// Don't fail the turn just because planning failed; log and proceed.
		rr.Warnings = append(rr.Warnings, "plan proposal failed: "+err.Error())
		return nil
	}
	if plan == nil {
		// Planner declined to propose — fine, continue without a plan.
		return nil
	}
	if _, err := r.engine.Execution.Propose(ctx, *plan); err != nil {
		rr.Warnings = append(rr.Warnings, "plan registration failed: "+err.Error())
		return nil
	}
	if r.autoApprovePlan {
		_ = r.engine.Execution.Approve(ctx, true)
	}
	rr.PlanProposed = plan
	return nil
}

// ── Phase 0: Orientation (cold start only) ───────────────────────────────────

func (r *Runtime) orientation(ctx context.Context, userMessage string, conv *Conversation, rr *RuntimeResult) error {
	if r.engine.HasPrompt() {
		if !r.engine.Prompt.Has(LayerBehavior) {
			r.engine.Prompt.Set(LayerBehavior, DefaultBehaviorPrompt)
		}
	}

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

	// Match and load skills → LayerSkills
	if err := r.matchAndLoadSkills(ctx, userMessage, conv, rr); err != nil {
		return err
	}

	// Build LayerSession: session context (time/location/user) + observations
	r.buildSessionLayer(ctx, conv, userMessage)
	return nil
}

// ── Warm turn refresh ────────────────────────────────────────────────────────

func (r *Runtime) warmRefresh(ctx context.Context, userMessage string, conv *Conversation) error {
	// On warm turns we only:
	//   1. Rebuild LayerSession (time changes every turn, observations may be new)
	//   2. Match for new skills not already loaded
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
	seen := make(map[string]bool) // dedup within this load cycle

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
		// Load required dependencies first (recursive, depth-limited)
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
		// Apply token cap on LayerSkills to prevent system prompt overflow
		// when many skills are loaded simultaneously. Uses the same tokenizer
		// as the rest of the budget enforcement.
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

// buildSessionLayer assembles LayerSession from the SessionContextProvider
// (time, location, user info) and the ObservationStore (recent observations).
// Both pieces are independent — either may produce content. Combined, they
// form the situational awareness layer the agent uses every turn.
//
// The layer is rebuilt fresh each call: session context is new each turn,
// and relevant observations depend on the current message.
func (r *Runtime) buildSessionLayer(ctx context.Context, conv *Conversation, userMessage string) {
	if !r.engine.HasPrompt() {
		return
	}
	var b strings.Builder

	// Session context: time, location, user info, surface
	if r.sessionContext != nil {
		if sc, err := r.sessionContext.Get(ctx, conv); err == nil && sc != nil {
			if rendered := sc.Format(); rendered != "" {
				b.WriteString(rendered)
				b.WriteString("\n\n")
			}
		}
	}

	// Recent observations relevant to current message
	if r.engine.HasObservations() {
		if obs, err := r.engine.Observations.Relevant(ctx, userMessage, 5); err == nil && len(obs) > 0 {
			b.WriteString("Recent observations:\n")
			for _, o := range obs {
				b.WriteString("- [")
				b.WriteString(o.Source)
				b.WriteString("] ")
				b.WriteString(o.Content)
				b.WriteString("\n")
			}
		}
	}

	if b.Len() == 0 {
		r.engine.Prompt.Clear(LayerSession)
		return
	}
	r.engine.Prompt.Set(LayerSession, strings.TrimRight(b.String(), "\n"))
}

// ── Phase 2: Preparation ─────────────────────────────────────────────────────

func (r *Runtime) preparation(ctx context.Context, conv *Conversation, rr *RuntimeResult) error {
	if r.autoCheckpoint && r.engine.HasCheckpoints() {
		if cp, err := r.engine.Checkpoints.Create(ctx, "Pre-execution checkpoint"); err == nil {
			rr.CheckpointID = cp.ID
		}
	}

	// Real budget enforcement with optional compaction of dropped messages.
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
		// Inject compacted summary into memory layer so the agent retains context.
		if compResult.Summary != "" && r.engine.HasPrompt() {
			r.engine.Prompt.Append(LayerMemory, "\n\nContext summary (from earlier turns):\n"+compResult.Summary)
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

	// Inject ThinkingBudget into every ChatRequest if extended thinking is enabled.
	if r.thinkingBudget > 0 {
		thinkingBudget := r.thinkingBudget
		cfg.BuildRequest = func(systemPrompt string, messages []ChatMessage, tools *ToolRegistry) ChatRequest {
			// Build a minimal config to pass to defaultBuildRequest
			innerCfg := AgentLoopConfig{
				SystemPrompt: systemPrompt,
				Tools:        tools,
			}
			req := defaultBuildRequest(innerCfg, messages)
			req.ThinkingBudget = thinkingBudget
			return req
		}
	}

	// Single point of prompt assembly: apply mode → LayerMode, then Build once.
	// Pass as cfg.SystemPrompt so RunAgentLoopWithEngine treats it as caller-supplied
	// and skips its own Build path.
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

	// Apply OutputFilter to the LLM's response before it reaches the caller.
	// Symmetric counterpart of SafetyFilter (which inspects tool calls).
	// Block: replace with reason, mark stop reason. Transform: replace text.
	if r.outputFilter != nil && rr.Response != "" {
		verdict := r.outputFilter.Inspect(ctx, rr.Response)
		switch verdict.Decision {
		case OutputBlock:
			rr.Response = "[output blocked: " + verdict.Reason + "]"
			rr.StopReason = "output_blocked"
			rr.Warnings = append(rr.Warnings, "output blocked: "+verdict.Reason)
			loopResult.FinalContent = rr.Response
		case OutputTransform:
			rr.Response = verdict.NewOutput
			loopResult.FinalContent = rr.Response
			if verdict.Reason != "" {
				rr.Warnings = append(rr.Warnings, "output transformed: "+verdict.Reason)
			}
		}
	}
	return loopResult, nil
}

// ── Phase 5: Closure ─────────────────────────────────────────────────────────

func (r *Runtime) closure(ctx context.Context, userMessage string, loopResult *AgentLoopResult, conv *Conversation, rr *RuntimeResult) error {
	// Explicit memory triggers — supports create, replace, delete
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
				r.handleMemoryTrigger(ctx, layer, content, rr)
			}
		}
	}

	// Inferred memory writes with deduplication and layer metadata
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

	// Clear expired session observations so they don't bleed into the next turn.
	// Explicit and Inferred memory is persistent — only Session is ephemeral.
	if r.engine.HasObservations() {
		_ = r.engine.Observations.Expire(ctx)
	}

	return nil
}

// handleMemoryTrigger writes, replaces, or deletes a memory entry.
// Before creating, it searches for an existing similar entry to update — this
// prevents "I work at Acme" and "I now work at Beta" coexisting as duplicates.
// "FORGET: X" triggers search-and-delete of entries matching X.
func (r *Runtime) handleMemoryTrigger(ctx context.Context, layer MemoryLayer, content string, rr *RuntimeResult) {
	// Forget intent: delete matching entries
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

	// Update intent: find similar existing entry and replace
	existing, _ := r.engine.Memory.Search(ctx, ScopeUser, content)
	for _, entry := range existing {
		if entry.Content == "" || entry.Path == "" {
			continue
		}
		if stringSimilarity(entry.Content, content) >= 0.5 {
			if err := r.engine.Memory.StrReplace(ctx, ScopeUser, entry.Path, entry.Content, content); err == nil {
				rr.MemoryWritten = append(rr.MemoryWritten, entry.Path)
				return
			}
		}
	}

	// No similar entry found — create new
	path := fmt.Sprintf("/facts/%d.md", time.Now().UnixNano())
	if err := r.engine.Memory.Create(ctx, ScopeUser, path, content); err == nil {
		rr.MemoryWritten = append(rr.MemoryWritten, path)
	}
	_ = layer
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
	PlanProposed      *Plan              `json:"plan_proposed,omitempty"`
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
		"i no longer ", "i moved to ", "i now work at ", "i changed ", "i started ", "my name is ", "i am ",
		// Spanish
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

// evictMemoryToTokenBudget truncates memory content to fit within maxTokens.
// Strategy: prefer keeping the most recent entries (bottom of file) and
// truncate from the top. This mirrors how Claude handles long memory:
// recent facts are more relevant than old ones.
func evictMemoryToTokenBudget(content string, maxTokens int, tok Tokenizer) string {
	paragraphs := strings.Split(strings.TrimSpace(content), "\n\n")
	if len(paragraphs) == 0 {
		return content
	}

	// Work backwards (most recent first), accumulate until budget exhausted
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
		// Nothing fits — take the last paragraph and truncate it
		last := strings.TrimSpace(paragraphs[len(paragraphs)-1])
		return last
	}
	if len(kept) < len(paragraphs) {
		kept = append([]string{"[Earlier memory entries omitted — token budget exceeded]"}, kept...)
	}
	return strings.Join(kept, "\n\n")
}
