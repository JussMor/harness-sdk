package autobuild

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Subagent is an isolated agent loop that runs a focused task in parallel
// with the main conversation. Use subagents for:
//   - Research tasks (web search, deep reads)
//   - Validation tasks (cross-checking facts)
//   - Independent parallel exploration
//
// A subagent has its own conversation, its own loaded skills, its own
// observation store. It does NOT share memory writes with the parent —
// memory is read-only from the subagent's perspective. Results return to
// the parent as a structured SubagentResult.
type Subagent struct {
	// ID identifies this subagent for tracing.
	ID string

	// Task is the focused instruction to the subagent.
	// Should be self-contained — the subagent has no parent context.
	Task string

	// Engine is a (possibly stripped-down) Engine for this subagent.
	// Typically shares LLM and Memory with parent but has restricted Tools.
	Engine *Engine

	// Mode is the active mode for the subagent (e.g. "research", "validator").
	Mode string

	// MaxTurns caps the subagent loop. Default 10 (subagents should be focused).
	MaxTurns int

	// Timeout caps wall-clock duration. Default 60s.
	Timeout time.Duration
}

// SubagentResult is what a subagent returns to its parent.
type SubagentResult struct {
	ID         string         `json:"id"`
	Task       string         `json:"task"`
	Output     string         `json:"output"`
	Turns      int            `json:"turns"`
	Usage      TokenUsage     `json:"usage"`
	StopReason string         `json:"stop_reason"`
	Duration   time.Duration  `json:"duration_ms"`
	Error      error          `json:"-"`
	Trace      []ReasoningStep `json:"trace,omitempty"`
}

// Run executes the subagent and returns its result.
// Used directly when you need a single subagent.
func (s *Subagent) Run(ctx context.Context) *SubagentResult {
	start := time.Now()
	res := &SubagentResult{
		ID:   s.ID,
		Task: s.Task,
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	maxTurns := s.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 10
	}

	subCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cfg := AgentLoopConfig{
		MaxTurns:     maxTurns,
		SystemPrompt: fmt.Sprintf("You are a focused subagent. Complete this task and report concisely:\n\n%s", s.Task),
	}

	loopResult, err := RunAgentLoopWithEngine(subCtx, s.Engine, s.Mode, cfg, []ChatMessage{
		{Role: RoleUser, Content: s.Task},
	})
	res.Duration = time.Since(start)

	if err != nil {
		res.Error = err
		return res
	}

	res.Output = loopResult.FinalContent
	res.Turns = loopResult.TotalTurns
	res.Usage = loopResult.TotalUsage
	res.StopReason = loopResult.StopReason
	res.Trace = loopResult.ReasoningTrace
	return res
}

// RunSubagentsInParallel runs multiple subagents concurrently and returns
// results in the same order as input. Cancellation propagates through ctx.
//
// All subagents share the parent context's cancellation but have their own
// timeouts. Use this for fan-out patterns: research multiple sources,
// validate against multiple criteria, explore alternative approaches.
func RunSubagentsInParallel(ctx context.Context, agents []Subagent) []*SubagentResult {
	if len(agents) == 0 {
		return nil
	}
	if len(agents) == 1 {
		return []*SubagentResult{agents[0].Run(ctx)}
	}

	results := make([]*SubagentResult, len(agents))
	var wg sync.WaitGroup
	wg.Add(len(agents))

	for i := range agents {
		go func(idx int, agent Subagent) {
			defer wg.Done()
			results[idx] = agent.Run(ctx)
		}(i, agents[i])
	}

	wg.Wait()
	return results
}
