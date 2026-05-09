package autobuild

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Subagent is an isolated agent loop that runs a focused task, optionally
// sharing state with the parent through a persistent Conversation.
//
// Key properties:
//   - Configurable system prompt, mode, model, and tool set
//   - Persistent conversation — the coordinator can send follow-up messages
//     to the same subagent and it retains context from previous turns
//   - Timeout and max-turn caps prevent runaway subagents
type Subagent struct {
	// ID identifies this subagent for tracing.
	ID string

	// Task is the initial user message sent to the subagent.
	// Should be self-contained — the subagent has no implicit parent context.
	Task string

	// SystemPrompt overrides the default generic subagent prompt.
	// Use this to give subagents distinct personas:
	//   "You are a code reviewer. Focus on security and correctness."
	//   "You are a research agent. Search and synthesize, never guess."
	// Empty → defaults to a generic focused-subagent prompt.
	SystemPrompt string

	// Engine is a (possibly stripped-down) Engine for this subagent.
	// Typically shares LLM and Memory with parent but has restricted Tools.
	Engine *Engine

	// Mode is the active mode for the subagent (e.g. "research", "validator").
	Mode string

	// Model overrides the engine's default model for this subagent.
	// Useful for coordinator (Opus) + specialist (Haiku) patterns.
	Model string

	// MaxTurns caps the subagent loop. Default 10.
	MaxTurns int

	// Timeout caps wall-clock duration. Default 60s.
	Timeout time.Duration

	// Conversation is the persistent conversation for this subagent.
	// When set, the subagent retains state across multiple Run() calls,
	// enabling coordinator follow-ups. When nil, each Run() starts fresh.
	Conversation *Conversation
}

// SubagentResult is what a subagent returns to its parent.
type SubagentResult struct {
	ID           string          `json:"id"`
	Task         string          `json:"task"`
	Output       string          `json:"output"`
	Turns        int             `json:"turns"`
	Usage        TokenUsage      `json:"usage"`
	StopReason   string          `json:"stop_reason"`
	Duration     time.Duration   `json:"duration_ms"`
	Error        error           `json:"-"`
	Trace        []ReasoningStep `json:"trace,omitempty"`
	// Model is the model name used by this subagent (bare, no routing prefix).
	// Populated from Subagent.Model if set.
	Model        string          `json:"model,omitempty"`
	// SystemPrompt is the system prompt used by this subagent, if overridden.
	// Populated from Subagent.SystemPrompt if set.
	SystemPrompt string          `json:"system_prompt,omitempty"`
}

// Run executes the subagent task and returns the result.
// If Conversation is set, appends to the existing context (persistent mode).
// If Conversation is nil, starts a fresh conversation each call.
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

	engine := s.Engine

	systemPrompt := s.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = fmt.Sprintf(
			"You are a focused subagent. Complete this task concisely and report your findings:\n\n%s",
			s.Task,
		)
	}

	// Resolve or create conversation
	conv := s.Conversation
	if conv == nil {
		conv = NewConversation(fmt.Sprintf("subagent-%s-%d", s.ID, time.Now().UnixNano()))
	}

	cfg := AgentLoopConfig{
		MaxTurns:     maxTurns,
		SystemPrompt: systemPrompt,
	}
	if s.Model != "" {
		cfg.Model = s.Model
	}

	loopResult, err := RunAgentLoopWithEngine(subCtx, engine, s.Mode, cfg, append(
		conv.Messages,
		ChatMessage{Role: RoleUser, Content: s.Task},
	))
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
	// Surface the model and system prompt so callers can display them
	if s.Model != "" {
		res.Model = s.Model
	}
	if s.SystemPrompt != "" {
		res.SystemPrompt = s.SystemPrompt
	}

	// Persist conversation for follow-ups when Conversation is set
	if s.Conversation != nil {
		s.Conversation.AppendUser(s.Task)
		s.Conversation.AppendAssistant(res.Output)
	}

	return res
}

// SendFollowUp sends an additional message to a persistent subagent.
// Requires s.Conversation to be set (returns error otherwise).
// This is the mechanism for coordinator follow-ups without starting over.
//
//	subagent.Conversation = autobuild.NewConversation("reviewer-thread")
//	result1 := subagent.Run(ctx)
//	subagent.Task = "Focus on the auth module specifically"
//	result2 := subagent.SendFollowUp(ctx)  // retains context from result1
func (s *Subagent) SendFollowUp(ctx context.Context, message string) *SubagentResult {
	if s.Conversation == nil {
		s.Conversation = NewConversation(fmt.Sprintf("subagent-%s-%d", s.ID, time.Now().UnixNano()))
	}
	orig := s.Task
	s.Task = message
	result := s.Run(ctx)
	s.Task = orig
	return result
}

// RunSubagentsInParallel runs multiple subagents concurrently and returns
// results in the same order as input. Cancellation propagates through ctx.
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
