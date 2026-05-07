package autobuild

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// StreamEvent is one chunk emitted during a streaming response.
// Different event types carry different payloads — discriminate by Type.
type StreamEvent struct {
	Type StreamEventType `json:"type"`

	// Delta is the incremental text chunk for Type=StreamEventDelta.
	Delta string `json:"delta,omitempty"`

	// Thinking is an incremental chunk of extended thinking content.
	// Only set for Type=StreamEventThinking.
	Thinking string `json:"thinking,omitempty"`

	// ToolCall is set when Type=StreamEventToolCall.
	ToolCall *ToolCallEntry `json:"tool_call,omitempty"`

	// ToolResult is set when Type=StreamEventToolResult.
	ToolResult *ToolResult `json:"tool_result,omitempty"`

	// Plan is set when Type=StreamEventPlanProposed.
	Plan *Plan `json:"plan,omitempty"`

	// SubagentResult is set when Type=StreamEventSubagentResult.
	SubagentResult *SubagentResult `json:"subagent_result,omitempty"`

	// ConfirmationRequest is set when Type=StreamEventConfirmationRequired.
	// The agent loop is paused. The consumer must call ApprovalGate.Respond
	// to unblock it.
	ConfirmationRequest *ApprovalRequest `json:"confirmation_request,omitempty"`

	// Final is the complete accumulated response when Type=StreamEventDone.
	Final *AgentLoopResult `json:"final,omitempty"`

	// Error is set when Type=StreamEventError.
	Error error `json:"-"`
}

// StreamEventType discriminates between event kinds.
type StreamEventType string

const (
	// StreamEventDelta is an incremental text chunk from the LLM.
	StreamEventDelta StreamEventType = "delta"

	// StreamEventThinking is an incremental chunk of extended thinking content.
	// Only emitted when ThinkingBudget > 0 and the provider supports it.
	// Thinking content is the model's internal reasoning — not shown to users
	// by default. Use for debugging, tracing, or advanced UX.
	StreamEventThinking StreamEventType = "thinking"

	// StreamEventConfirmationRequired is emitted when a tool call needs human
	// approval. The agent loop is paused until ApprovalGate.Respond is called.
	// StreamEvent.ConfirmationRequest contains the request details.
	StreamEventConfirmationRequired StreamEventType = "confirmation_required"

	// StreamEventConfirmationResolved is emitted after a confirmation_required
	// is resolved — approved or rejected.
	// StreamEvent.ConfirmationRequest.ID identifies which request resolved.
	StreamEventConfirmationResolved StreamEventType = "confirmation_resolved"

	// StreamEventToolCall is emitted when the LLM decides to call a tool.
	// Tool execution happens between this event and the next StreamEventToolResult.
	StreamEventToolCall StreamEventType = "tool_call"

	// StreamEventToolResult is emitted after a tool returns.
	StreamEventToolResult StreamEventType = "tool_result"

	// StreamEventTurnComplete fires when a single LLM turn finishes
	// (one request/response cycle within the agent loop).
	StreamEventTurnComplete StreamEventType = "turn_complete"

	// StreamEventPlanProposed fires when the alignment phase proposes an
	// execution plan. Consumers can display plan status or trigger custom
	// subagent orchestration at the application layer.
	StreamEventPlanProposed StreamEventType = "plan_proposed"

	// StreamEventSubagentResult fires once per subagent as they complete
	// during plan fan-out execution. Consumers can stream partial results
	// to the user as each parallel task finishes.
	StreamEventSubagentResult StreamEventType = "subagent_result"

	// StreamEventDone fires once when the entire agent loop has finished.
	// Final field is populated.
	StreamEventDone StreamEventType = "done"

	// StreamEventError reports a fatal error. No further events follow.
	StreamEventError StreamEventType = "error"
)

// StreamingLLMProvider extends LLMProvider with token-by-token streaming.
// Implement this for providers that support streaming (Anthropic, OpenAI).
// LLMProvider implementations that don't support streaming will simply
// not be used in streaming flows.
type StreamingLLMProvider interface {
	LLMProvider

	// ChatStream sends a request and returns a channel of stream events.
	// The channel closes when the response is complete (after StreamEventDone)
	// or when an error occurs (after StreamEventError).
	//
	// Cancel via ctx — implementations must respect cancellation promptly.
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}

// ── Runtime streaming ────────────────────────────────────────────────────────

// RunStream is the streaming counterpart of Runtime.Run. It returns a
// channel of StreamEvents that emit as the agent works.
//
// Phases run identically to Run (orientation → alignment → preparation →
// execution → verification → closure). Streaming only affects the LLM
// generation inside Execution — text deltas flow token by token as the
// model produces them.
//
// If the wired LLMProvider implements StreamingLLMProvider, real token-level
// streaming occurs. Otherwise, falls back to sentence-chunked emission of the
// full response — same API contract, degraded UX.
//
// Tool calls still execute synchronously during streaming:
//   StreamEventToolCall  → tool dispatch begins
//   StreamEventToolResult → tool returned, next LLM turn starts streaming
//   StreamEventDelta     → model generating response to tool result
//   StreamEventDone      → all turns complete
func (r *Runtime) RunStream(ctx context.Context, conv *Conversation, userMessage string) (<-chan StreamEvent, error) {
	out := make(chan StreamEvent, 64)

	streamProv, hasRealStream := r.engine.LLM.(StreamingLLMProvider)

	go func() {
		defer close(out)

		if !hasRealStream {
			// Fallback: run normally, emit sentence chunks
			result, err := r.Run(ctx, conv, userMessage)
			if err != nil {
				out <- StreamEvent{Type: StreamEventError, Error: err}
				return
			}
			for _, chunk := range chunkBySentence(result.Response) {
				select {
				case <-ctx.Done():
					out <- StreamEvent{Type: StreamEventError, Error: ctx.Err()}
					return
				case out <- StreamEvent{Type: StreamEventDelta, Delta: chunk}:
				}
			}
			out <- StreamEvent{Type: StreamEventDone}
			return
		}

		// Real streaming path:
		// 1. Run all phases except Execution normally
		// 2. In Execution, intercept the LLM call to use ChatStream
		// 3. Handle tool calls (dispatch → next streaming turn) in a loop
		if err := r.runStreamInternal(ctx, conv, userMessage, streamProv, out); err != nil {
			out <- StreamEvent{Type: StreamEventError, Error: err}
		}
	}()

	return out, nil
}

// runStreamInternal executes the full 6-phase lifecycle with real streaming
// in the Execution phase. Tool calls are dispatched synchronously between
// streaming turns.
func (r *Runtime) runStreamInternal(
	ctx context.Context,
	conv *Conversation,
	userMessage string,
	streamProv StreamingLLMProvider,
	out chan<- StreamEvent,
) error {
	// ── Phases 0-2: identical to Run ──
	conv.AppendUser(userMessage)

	if r.wellbeing != nil {
		signal := r.wellbeing.Detect(userMessage)
		if signal.Detected && signal.Severity >= WellbeingSeverityHigh {
			resp := wellbeingResponse(signal)
			out <- StreamEvent{Type: StreamEventDelta, Delta: resp}
			out <- StreamEvent{Type: StreamEventDone}
			conv.AppendAssistant(resp)
			conv.IncrementTurn()
			if r.store != nil {
				_ = r.store.Save(ctx, conv)
			}
			return nil
		}
	}

	if r.engine.HasExecution() {
		_ = r.engine.Execution.SetPhase(ctx, PhaseOrientation)
	}
	if conv.IsCold() {
		rr := &RuntimeResult{}
		if err := r.orientation(ctx, userMessage, conv, rr); err != nil {
			return fmt.Errorf("orientation: %w", err)
		}
	} else {
		if err := r.warmRefresh(ctx, userMessage, conv); err != nil {
			return fmt.Errorf("warm refresh: %w", err)
		}
	}

	_ = r.advancePhase(ctx, PhaseAlignment)
	// Alignment: planner (non-streaming, cheap)
	var proposedPlan *Plan
	if r.planner != nil && r.engine.HasExecution() && r.planner.ShouldPlan(ctx, userMessage, conv) {
		plan, _ := r.planner.Propose(ctx, userMessage, conv)
		if plan != nil {
			if _, err := r.engine.Execution.Propose(ctx, *plan); err == nil && r.autoApprovePlan {
				_ = r.engine.Execution.Approve(ctx, true)
			}
			proposedPlan = plan
			out <- StreamEvent{Type: StreamEventPlanProposed, Plan: plan}
		}
	}

	_ = r.advancePhase(ctx, PhasePreparation)
	prepRR := &RuntimeResult{}
	if err := r.preparation(ctx, conv, prepRR); err != nil {
		return fmt.Errorf("preparation: %w", err)
	}

	_ = r.advancePhase(ctx, PhaseExecution)

	// ── Streaming Execution loop ──
	// Mirrors agent_loop.go but uses ChatStream instead of Chat.
	var finalResponse string
	var totalUsage TokenUsage
	messages := conv.Messages

	// Build system prompt once
	systemPrompt := ""
	if r.engine.HasPrompt() {
		if r.mode != "" && r.engine.HasModes() {
			if mode, err := r.engine.Modes.Get(ctx, r.mode); err == nil {
				r.engine.Prompt.Set(LayerMode, mode.PromptContent)
			}
		}
		systemPrompt = r.engine.Prompt.Build()
	}

	dispatcher := NewToolDispatcher(r.engine.Tools, r.engine.Sandbox)

	// If human-in-the-loop is active, forward ApprovalRequests to the stream
	// as StreamEventConfirmationRequired events. This runs in a separate goroutine
	// because HumanApprovalFilter.Inspect blocks the main loop while waiting —
	// we need to forward the request to the frontend before unblocking.
	if r.approvalGate != nil {
		go func() {
			for req := range r.approvalGate.Requests() {
				select {
				case <-ctx.Done():
					return
				case out <- StreamEvent{
					Type:                StreamEventConfirmationRequired,
					ConfirmationRequest: &req,
				}:
				}
			}
		}()
	}

	for turn := 0; turn < 50; turn++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		req := ChatRequest{
			Model:    "",
			Messages: buildRequestMessages(systemPrompt, messages),
		}
		if r.engine.HasTools() {
			req.Tools = r.engine.Tools.ToolDefs()
		}

		events, err := streamProv.ChatStream(ctx, req)
		if err != nil {
			return fmt.Errorf("stream turn %d: %w", turn, err)
		}

		// Collect this turn's output
		var turnText strings.Builder
		var turnToolCalls []ToolCallEntry
		var turnDone bool

		for ev := range events {
			switch ev.Type {
			case StreamEventDelta:
				turnText.WriteString(ev.Delta)
				// Forward to consumer
				select {
				case <-ctx.Done():
					return ctx.Err()
				case out <- ev:
				}

			case StreamEventToolCall:
				turnToolCalls = append(turnToolCalls, *ev.ToolCall)
				out <- ev

			case StreamEventDone:
				if ev.Final != nil {
					totalUsage.PromptTokens += ev.Final.TotalUsage.PromptTokens
					totalUsage.CompletionTokens += ev.Final.TotalUsage.CompletionTokens
					totalUsage.TotalTokens += ev.Final.TotalUsage.TotalTokens
				}
				turnDone = true

			case StreamEventError:
				return fmt.Errorf("stream event error: %w", ev.Error)
			}
		}

		// Append assistant turn to message history
		assistantMsg := ChatMessage{
			Role:      RoleAssistant,
			Content:   turnText.String(),
			ToolCalls: turnToolCalls,
		}
		messages = append(messages, assistantMsg)
		if turnText.Len() > 0 {
			finalResponse = turnText.String()
		}

		// If no tool calls, we're done
		if len(turnToolCalls) == 0 || !turnDone {
			break
		}

		// Dispatch tool calls — apply safety filter before each call
		var sandboxID string
		// Filter tool calls through safety
		var allowedCalls []ToolCallEntry
		for _, call := range turnToolCalls {
			if r.safety != nil {
				verdict := r.safety.Inspect(ctx, call)
				// If HIL was involved, emit resolved event
				if r.approvalGate != nil && (verdict.Decision == SafetyAllow || verdict.Decision == SafetyTransform) {
					out <- StreamEvent{
						Type:                StreamEventConfirmationResolved,
						ConfirmationRequest: &ApprovalRequest{ToolCall: call},
					}
				}
				if verdict.Decision == SafetyBlock {
					// Emit blocked result as tool_result to the consumer
					blocked := ToolResult{
						Name:       call.Name,
						ToolCallID: call.ID,
						Content:    "[blocked by safety filter: " + verdict.Reason + "]",
					}
					out <- StreamEvent{Type: StreamEventToolResult, ToolResult: &blocked}
					messages = append(messages, ChatMessage{
						Role:       RoleTool,
						Content:    blocked.Content,
						ToolCallID: blocked.ToolCallID,
					})
					continue
				}
				if verdict.Decision == SafetyTransform {
					call.Arguments = verdict.NewArgs
				}
			}
			allowedCalls = append(allowedCalls, call)
		}

		results := dispatcher.DispatchParallel(ctx, allowedCalls, sandboxID)
		for i, result := range results {
			// Apply observation recording
			if r.engine.HasObservations() && r.observationFilt != nil && i < len(allowedCalls) {
				obs := r.observationFilt(allowedCalls[i], result)
				if obs.Content != "" {
					_ = r.engine.Observations.Record(ctx, obs)
				}
			}
			// Emit tool result event
			out <- StreamEvent{Type: StreamEventToolResult, ToolResult: &result}
			// Append to messages for next turn
			messages = append(messages, ChatMessage{
				Role:       RoleTool,
				Content:    result.Content,
				ToolCallID: result.ToolCallID,
			})
		}
	}

	// Apply output filter to final response
	if r.outputFilter != nil && finalResponse != "" {
		verdict := r.outputFilter.Inspect(ctx, finalResponse)
		switch verdict.Decision {
		case OutputBlock:
			finalResponse = "[output blocked: " + verdict.Reason + "]"
		case OutputTransform:
			finalResponse = verdict.NewOutput
		}
	}

	// ── Subagent fan-out from proposed plan ──
	if proposedPlan != nil && len(proposedPlan.Executables) >= 2 {
		ready := proposedPlan.NextReady()
		if len(ready) >= 2 {
			r.runStreamSubagents(ctx, ready, out)
		}
	}

	// ── Phases 4-5: Verification + Closure ──
	_ = r.advancePhase(ctx, PhaseVerification)
	_ = r.advancePhase(ctx, PhaseClosure)

	conv.AppendAssistant(finalResponse)

	// Closure: memory writes
	closureRR := &RuntimeResult{}
	_ = r.closure(ctx, userMessage, &AgentLoopResult{
		FinalContent: finalResponse,
		TotalUsage:   totalUsage,
	}, conv, closureRR)

	conv.IncrementTurn()
	if r.store != nil {
		_ = r.store.Save(ctx, conv)
	}

	out <- StreamEvent{Type: StreamEventDone}
	return nil
}

// runStreamSubagents executes plan executables as parallel subagents, emitting
// a StreamEventSubagentResult as each one completes.
func (r *Runtime) runStreamSubagents(ctx context.Context, executables []Executable, out chan<- StreamEvent) {
	// Build subagents from ready executables
	agents := make([]Subagent, 0, len(executables))
	for _, exec := range executables {
		task := strings.TrimSpace(exec.Description)
		if task == "" {
			task = strings.TrimSpace(exec.Name)
		}
		if task == "" {
			continue
		}
		agents = append(agents, Subagent{
			ID:       exec.ID,
			Task:     task,
			Engine:   r.engine,
			MaxTurns: 4,
			Timeout:  30 * 1000000000, // 30s
		})
	}
	if len(agents) < 2 {
		return
	}

	// Run in parallel, emit results as they arrive via a channel
	results := make(chan *SubagentResult, len(agents))
	var wg sync.WaitGroup
	wg.Add(len(agents))
	for i := range agents {
		go func(agent Subagent) {
			defer wg.Done()
			results <- agent.Run(ctx)
		}(agents[i])
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	for res := range results {
		out <- StreamEvent{Type: StreamEventSubagentResult, SubagentResult: res}
	}
}

// buildRequestMessages prepends a system message to the conversation history.
func buildRequestMessages(systemPrompt string, messages []ChatMessage) []ChatMessage {
	if systemPrompt == "" {
		return messages
	}
	out := make([]ChatMessage, 0, len(messages)+1)
	out = append(out, ChatMessage{Role: RoleSystem, Content: systemPrompt})
	out = append(out, messages...)
	return out
}

// chunkBySentence splits text on sentence-ending punctuation while preserving
// the punctuation in the chunk. Used as a coarse default for streaming pacing.
func chunkBySentence(text string) []string {
	if text == "" {
		return nil
	}
	var chunks []string
	var current strings.Builder
	for i, r := range text {
		current.WriteRune(r)
		if r == '.' || r == '!' || r == '?' || r == '\n' {
			// Look ahead for whitespace or end
			next := i + 1
			if next >= len(text) || text[next] == ' ' || text[next] == '\n' || text[next] == '\t' {
				chunk := strings.TrimSpace(current.String())
				if chunk != "" {
					chunks = append(chunks, chunk+" ")
				}
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		final := strings.TrimSpace(current.String())
		if final != "" {
			chunks = append(chunks, final)
		}
	}
	return chunks
}

// ── Stream collection helpers ────────────────────────────────────────────────

// CollectStream drains a stream channel into a complete response string and
// the final AgentLoopResult. Useful for tests and code that wants to use
// RunStream but doesn't actually want to handle deltas.
func CollectStream(events <-chan StreamEvent) (string, *AgentLoopResult, error) {
	var b strings.Builder
	var final *AgentLoopResult
	for ev := range events {
		switch ev.Type {
		case StreamEventDelta:
			b.WriteString(ev.Delta)
		case StreamEventDone:
			final = ev.Final
		case StreamEventError:
			return b.String(), final, ev.Error
		}
	}
	return b.String(), final, nil
}

// FanOutStream broadcasts each event to multiple consumers. Each consumer
// receives the same events in the same order. Useful when one consumer
// renders the UI and another logs to telemetry.
//
// The returned channels close when the source closes. Slow consumers
// block the source — buffer with goroutines if that's a concern.
func FanOutStream(source <-chan StreamEvent, consumers int) []<-chan StreamEvent {
	if consumers <= 0 {
		return nil
	}
	outs := make([]chan StreamEvent, consumers)
	for i := range outs {
		outs[i] = make(chan StreamEvent, 16)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range source {
			for _, out := range outs {
				out <- ev
			}
		}
		for _, out := range outs {
			close(out)
		}
	}()

	readonly := make([]<-chan StreamEvent, consumers)
	for i, ch := range outs {
		readonly[i] = ch
	}
	return readonly
}
