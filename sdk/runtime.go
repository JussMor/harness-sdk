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
//   - Verify context budget before each LLM call
//   - Surface relevant observations into the prompt
//   - Detect memory write triggers in user messages
//   - Advance phase based on LLM signals
//   - Filter tool results into observations
//
// Runtime does all of this automatically. The consumer only provides the
// user message and gets back a response.
//
// Typical usage:
//
//	engine := autobuild.NewWithDefaults(128_000)
//	engine.LLM = myLLM
//	engine.Memory = myMemory
//	engine.Tools = myTools
//
//	runtime := autobuild.NewRuntime(engine)
//	result, err := runtime.Run(ctx, "Help me refactor auth")
type Runtime struct {
	engine *Engine
	mode   string // active mode ID

	// Configurable behavior
	maxSkills       int     // max skills to load per turn (default 3)
	skillThreshold  float64 // min match score to load (default 0.3)
	autoAdvance     bool    // auto-advance phase based on LLM signals (default true)
	autoCheckpoint  bool    // auto-create checkpoint before Execution (default true)
	memoryRoots     []string // paths to read at orientation (default ["/"])
	observationFilt ObservationFilter
	memoryTrigger   MemoryTriggerDetector
}

// ObservationFilter decides if a tool result should become an Observation.
// Return Observation with non-empty Content to record. Return zero value to skip.
type ObservationFilter func(call ToolCallEntry, result ToolResult) Observation

// MemoryTriggerDetector inspects a user message for memory write intent.
// Returns the layer to write at and the content if a trigger is detected.
// Returns empty layer if no trigger.
type MemoryTriggerDetector func(message string) (layer MemoryLayer, content string, detected bool)

// NewRuntime creates a Runtime over an Engine with sensible defaults.
// The Engine should already have at minimum an LLM provider.
func NewRuntime(engine *Engine) *Runtime {
	return &Runtime{
		engine:          engine,
		maxSkills:       3,
		skillThreshold:  0.3,
		autoAdvance:     true,
		autoCheckpoint:  true,
		memoryRoots:     []string{"/"},
		observationFilt: DefaultObservationFilter,
		memoryTrigger:   DefaultMemoryTriggerDetector,
	}
}

// WithMode sets the active mode for subsequent runs.
func (r *Runtime) WithMode(modeID string) *Runtime {
	r.mode = modeID
	return r
}

// WithSkillThreshold overrides the minimum match score for auto-loading skills.
// Default is 0.3.
func (r *Runtime) WithSkillThreshold(threshold float64) *Runtime {
	r.skillThreshold = threshold
	return r
}

// WithMaxSkills caps how many skills auto-load per turn. Default is 3.
func (r *Runtime) WithMaxSkills(n int) *Runtime {
	r.maxSkills = n
	return r
}

// WithObservationFilter replaces the default tool-result-to-observation filter.
func (r *Runtime) WithObservationFilter(f ObservationFilter) *Runtime {
	r.observationFilt = f
	return r
}

// WithMemoryTrigger replaces the default memory write trigger detector.
func (r *Runtime) WithMemoryTrigger(d MemoryTriggerDetector) *Runtime {
	r.memoryTrigger = d
	return r
}

// Run executes a full conversation turn through the 6-phase lifecycle:
// Orientation → Alignment → Preparation → Execution → Verification → Closure.
//
// Each phase wires the relevant providers automatically. The agent loop
// runs inside Execution. Memory writes happen in Closure.
func (r *Runtime) Run(ctx context.Context, userMessage string) (*RuntimeResult, error) {
	if !r.engine.HasLLM() {
		return nil, fmt.Errorf("runtime: no LLM provider — set engine.LLM")
	}

	rr := &RuntimeResult{
		StartedAt: time.Now(),
	}

	// ── Phase 0: Orientation ──────────────────────────────────────────
	if err := r.orientation(ctx, userMessage, rr); err != nil {
		return rr, fmt.Errorf("orientation: %w", err)
	}
	r.advance(ctx)

	// ── Phase 1: Alignment ────────────────────────────────────────────
	// (Plan proposal happens here if user message indicates complex task.
	// For now, runtime defers plan decisions to the LLM during Execution.)
	r.advance(ctx)

	// ── Phase 2: Preparation ──────────────────────────────────────────
	if err := r.preparation(ctx, rr); err != nil {
		return rr, fmt.Errorf("preparation: %w", err)
	}
	r.advance(ctx)

	// ── Phase 3: Execution ────────────────────────────────────────────
	if err := r.execution(ctx, userMessage, rr); err != nil {
		return rr, fmt.Errorf("execution: %w", err)
	}
	r.advance(ctx)

	// ── Phase 4: Verification ─────────────────────────────────────────
	// (Verification logic is application-specific. Runtime exposes hooks
	// for the consumer to verify; default is to trust the LLM's "complete".)
	r.advance(ctx)

	// ── Phase 5: Closure ──────────────────────────────────────────────
	if err := r.closure(ctx, userMessage, rr); err != nil {
		return rr, fmt.Errorf("closure: %w", err)
	}

	rr.CompletedAt = time.Now()
	return rr, nil
}

// ── Phase 0: Orientation ─────────────────────────────────────────────────────

// orientation reads memory and matches skills, populating the SystemPromptBuilder.
// This is the step that prevents redundant questions and wrong assumptions.
func (r *Runtime) orientation(ctx context.Context, userMessage string, rr *RuntimeResult) error {
	if r.engine.HasPrompt() {
		// Always set the behavior layer if not already set
		if !r.engine.Prompt.Has(LayerBehavior) {
			r.engine.Prompt.Set(LayerBehavior, DefaultBehaviorPrompt)
		}
	}

	// Read memory → LayerMemory
	if r.engine.HasMemory() && r.engine.HasPrompt() {
		var memContent strings.Builder
		for _, root := range r.memoryRoots {
			content, err := r.engine.Memory.View(ctx, ScopeUser, root)
			if err == nil && content != "" {
				memContent.WriteString(content)
				memContent.WriteString("\n\n")
			}
			content, err = r.engine.Memory.View(ctx, ScopeProject, root)
			if err == nil && content != "" {
				memContent.WriteString(content)
				memContent.WriteString("\n\n")
			}
		}
		if memContent.Len() > 0 {
			r.engine.Prompt.Set(LayerMemory, memContent.String())
			rr.MemoryRead = true
		}
	}

	// Match and load skills → LayerSkills
	if r.engine.HasSkills() && r.engine.HasPrompt() {
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
				break // matches are sorted by score desc
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
			rr.SkillsLoaded = append(rr.SkillsLoaded, skill.Name)
			loaded++
		}
		if skillContent.Len() > 0 {
			r.engine.Prompt.Set(LayerSkills, skillContent.String())
		}
	}

	// Surface relevant observations → LayerSession
	if r.engine.HasObservations() && r.engine.HasPrompt() {
		obs, err := r.engine.Observations.Relevant(ctx, userMessage, 5)
		if err == nil && len(obs) > 0 {
			var sessionContent strings.Builder
			sessionContent.WriteString("Recent observations from this session:\n")
			for _, o := range obs {
				sessionContent.WriteString("- [")
				sessionContent.WriteString(o.Source)
				sessionContent.WriteString("] ")
				sessionContent.WriteString(o.Content)
				sessionContent.WriteString("\n")
			}
			r.engine.Prompt.Append(LayerSession, sessionContent.String())
		}
	}

	return nil
}

// ── Phase 2: Preparation ─────────────────────────────────────────────────────

// preparation creates a checkpoint and verifies context budget before execution.
func (r *Runtime) preparation(ctx context.Context, rr *RuntimeResult) error {
	if r.autoCheckpoint && r.engine.HasCheckpoints() {
		cp, err := r.engine.Checkpoints.Create(ctx, "Pre-execution checkpoint")
		if err == nil {
			rr.CheckpointID = cp.ID
		}
	}

	// Verify budget. If overflow, evict oldest skills.
	if r.engine.HasBudget() && r.engine.HasPrompt() {
		assembled := r.engine.Prompt.Build()
		approxTokens := EstimateTokens(assembled)
		if approxTokens > r.engine.Budget.Available() {
			rr.Warnings = append(rr.Warnings,
				fmt.Sprintf("context budget warning: %d tokens vs %d available",
					approxTokens, r.engine.Budget.Available()))
		}
	}

	return nil
}

// ── Phase 3: Execution ───────────────────────────────────────────────────────

// execution runs the agent loop with all wiring connected.
func (r *Runtime) execution(ctx context.Context, userMessage string, rr *RuntimeResult) error {
	cfg := AgentLoopConfig{
		MaxTurns: 50,
		// Filter tool results into observations
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

	// Use the assembled prompt if a builder exists
	if r.engine.HasPrompt() {
		cfg.SystemPrompt = r.engine.Prompt.Build()
	}

	messages := []ChatMessage{
		{Role: RoleUser, Content: userMessage},
	}

	loopResult, err := RunAgentLoopWithEngine(ctx, r.engine, r.mode, cfg, messages)
	if err != nil {
		return err
	}

	rr.Response = loopResult.FinalContent
	rr.Turns = loopResult.TotalTurns
	rr.Usage = loopResult.TotalUsage
	rr.StopReason = loopResult.StopReason
	rr.Trace = loopResult.ReasoningTrace

	return nil
}

// ── Phase 5: Closure ─────────────────────────────────────────────────────────

// closure detects memory write triggers and persists if needed.
func (r *Runtime) closure(ctx context.Context, userMessage string, rr *RuntimeResult) error {
	if !r.engine.HasMemory() || r.memoryTrigger == nil {
		return nil
	}

	layer, content, detected := r.memoryTrigger(userMessage)
	if !detected || content == "" {
		return nil
	}

	// Determine scope from layer
	scope := ScopeUser
	if layer == MemoryLayerSession {
		// Session-layer goes to ObservationStore, not memory
		if r.engine.HasObservations() {
			_ = r.engine.Observations.Record(ctx, Observation{
				Source:    "user_message",
				Content:   content,
				Relevance: 0.7,
			})
		}
		return nil
	}

	// Write as a new memory file under /facts/<timestamp>.md
	path := fmt.Sprintf("/facts/%d.md", time.Now().Unix())
	err := r.engine.Memory.Create(ctx, scope, path, content)
	if err == nil {
		rr.MemoryWritten = append(rr.MemoryWritten, path)
	}
	return nil
}

// ── advance ──────────────────────────────────────────────────────────────────

func (r *Runtime) advance(ctx context.Context) {
	if r.autoAdvance && r.engine.HasExecution() {
		_ = r.engine.Execution.Advance(ctx)
	}
}

// ── RuntimeResult ────────────────────────────────────────────────────────────

// RuntimeResult is the outcome of a Runtime.Run call.
type RuntimeResult struct {
	Response      string         `json:"response"`
	Turns         int            `json:"turns"`
	Usage         TokenUsage     `json:"usage"`
	StopReason    string         `json:"stop_reason"`
	Trace         []ReasoningStep `json:"trace"`
	SkillsLoaded  []string       `json:"skills_loaded"`
	MemoryRead    bool           `json:"memory_read"`
	MemoryWritten []string       `json:"memory_written"`
	CheckpointID  string         `json:"checkpoint_id,omitempty"`
	Warnings      []string       `json:"warnings,omitempty"`
	StartedAt     time.Time      `json:"started_at"`
	CompletedAt   time.Time      `json:"completed_at"`
}

// ── DefaultObservationFilter ─────────────────────────────────────────────────

// DefaultObservationFilter records tool results that look like they have
// reusable information: search results, fetched content, file reads.
// Skips trivial results (status checks, simple confirmations).
func DefaultObservationFilter(call ToolCallEntry, result ToolResult) Observation {
	if result.Error != nil {
		return Observation{} // skip errors
	}
	content := strings.TrimSpace(result.Content)
	if len(content) < 50 {
		return Observation{} // skip trivial results
	}
	// Cap content size — observations are summaries, not full dumps
	if len(content) > 2000 {
		content = content[:2000] + "..."
	}
	return Observation{
		Source:    call.Name,
		Content:   content,
		Relevance: 0.6,
		CreatedAt: time.Now(),
	}
}

// ── DefaultMemoryTriggerDetector ─────────────────────────────────────────────

// DefaultMemoryTriggerDetector mirrors Claude's memory_user_edits trigger logic:
// explicit "remember that X" and implicit state-change phrases.
func DefaultMemoryTriggerDetector(message string) (MemoryLayer, string, bool) {
	lower := strings.ToLower(strings.TrimSpace(message))

	// Explicit triggers — direct request to store
	explicitTriggers := []string{
		"remember that ",
		"please remember ",
		"don't forget that ",
		"don't forget ",
		"update your memory ",
		"save that ",
	}
	for _, trigger := range explicitTriggers {
		if idx := strings.Index(lower, trigger); idx >= 0 {
			content := strings.TrimSpace(message[idx+len(trigger):])
			if content != "" {
				return MemoryLayerExplicit, content, true
			}
		}
	}

	// Implicit triggers — state change
	implicitTriggers := []string{
		"i no longer ",
		"i moved to ",
		"i now work at ",
		"i changed ",
		"i started ",
	}
	for _, trigger := range implicitTriggers {
		if idx := strings.Index(lower, trigger); idx >= 0 {
			// Take the rest of the sentence (until period or end)
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

	// Forget triggers — explicit removal request (caller decides what to do)
	forgetTriggers := []string{"forget about ", "please forget "}
	for _, trigger := range forgetTriggers {
		if idx := strings.Index(lower, trigger); idx >= 0 {
			content := strings.TrimSpace(message[idx+len(trigger):])
			if content != "" {
				// Mark as inferred so caller can choose to delete instead of write
				return MemoryLayerInferred, "FORGET: " + content, true
			}
		}
	}

	return "", "", false
}

// ── EstimateTokens ───────────────────────────────────────────────────────────

// EstimateTokens returns a rough token count (chars/4 heuristic).
// Replace with a real tokenizer for production.
func EstimateTokens(text string) int {
	return len(text) / 4
}
