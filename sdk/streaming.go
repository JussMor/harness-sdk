package autobuild

import (
	"context"
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

	// AgentResult is set when Type=StreamEventAgentResult.
	AgentResult *AgentResult `json:"agent_result,omitempty"`

	// Interrupt is set when Type=StreamEventInterruptRequired or
	// Type=StreamEventInterruptResolved. Discriminate sub-variants on Kind.
	Interrupt *InterruptRequest `json:"interrupt,omitempty"`

	// Artifact is set when Type=StreamEventArtifactCreated or
	// Type=StreamEventArtifactUpdated. Carries a typed payload (file or
	// generative-UI component) the frontend should render alongside chat.
	Artifact *Artifact `json:"artifact,omitempty"`

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
	StreamEventThinking StreamEventType = "thinking"

	// StreamEventInterruptRequired is emitted when the agent pauses awaiting
	// human input. StreamEvent.Interrupt carries the full request — discriminate
	// the kind via Interrupt.Kind (approval / question / form_input).
	StreamEventInterruptRequired StreamEventType = "interrupt_required"

	// StreamEventInterruptResolved is emitted when an interrupt is answered.
	StreamEventInterruptResolved StreamEventType = "interrupt_resolved"

	// StreamEventArtifactCreated is emitted when a tool/skill attaches a new
	// artifact (file or component) to the turn via EmitArtifact.
	StreamEventArtifactCreated StreamEventType = "artifact_created"

	// StreamEventArtifactUpdated is emitted when an existing artifact's content
	// or props change. The Artifact.ID matches a previously created artifact.
	StreamEventArtifactUpdated StreamEventType = "artifact_updated"

	// StreamEventToolCall is emitted when the LLM decides to call a tool.
	// Tool execution happens between this event and the next StreamEventToolResult.
	StreamEventToolCall StreamEventType = "tool_call"

	// StreamEventToolResult is emitted after a tool returns.
	StreamEventToolResult StreamEventType = "tool_result"

	// StreamEventTurnComplete fires when a single LLM turn finishes
	// (one request/response cycle within the agent loop).
	StreamEventTurnComplete StreamEventType = "turn_complete"

	// StreamEventAgentResult fires once per spawned Agent as it completes.
	// Consumers can stream partial results to the user as each parallel
	// Agent invocation finishes.
	StreamEventAgentResult StreamEventType = "agent_result"

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
// If the wired LLMProvider implements StreamingLLMProvider, real token-level
// streaming occurs. Otherwise, falls back to sentence-chunked emission of the
// full response — same API contract, degraded UX.
//
// Tool calls still execute synchronously during streaming:
//
//	StreamEventToolCall  → tool dispatch begins
//	StreamEventToolResult → tool returned, next LLM turn starts streaming
//	StreamEventDelta     → model generating response to tool result
//	StreamEventDone      → all turns complete
func (r *Runtime) RunStream(ctx context.Context, conv *Conversation, userMessage string) (<-chan StreamEvent, error) {
	out := make(chan StreamEvent, 64)

	streamProv, hasRealStream := r.engine.LLM.(StreamingLLMProvider)

	ctx = WithArtifactEmitter(ctx, func(a Artifact) {
		select {
		case <-ctx.Done():
		case out <- StreamEvent{Type: StreamEventArtifactCreated, Artifact: &a}:
		}
	})

	if r.interruptGate != nil {
		gate := r.interruptGate
		ctx = WithInterruptRequester(ctx, func(c context.Context, req InterruptRequest) (InterruptResponse, error) {
			return gate.Wait(c, req)
		})
	}

	go func() {
		defer close(out)

		if !hasRealStream {
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

		if err := r.runStreamInternal(ctx, conv, userMessage, streamProv, out); err != nil {
			out <- StreamEvent{Type: StreamEventError, Error: err}
		}
	}()

	return out, nil
}

// runStreamInternal executes orientation, streaming execution, and closure.
func (r *Runtime) runStreamInternal(
	ctx context.Context,
	conv *Conversation,
	userMessage string,
	streamProv StreamingLLMProvider,
	out chan<- StreamEvent,
) error {
	conv.AppendUser(userMessage)

	if conv.IsCold() {
		rr := &RuntimeResult{}
		if err := r.orientation(ctx, userMessage, conv, rr); err != nil {
			return err
		}
	} else {
		if err := r.warmRefresh(ctx, userMessage, conv); err != nil {
			return err
		}
	}

	prepRR := &RuntimeResult{}
	if err := r.preparation(ctx, conv, prepRR); err != nil {
		return err
	}

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

	// Forward interrupt requests to the stream
	if r.interruptGate != nil {
		go func() {
			for req := range r.interruptGate.Requests() {
				req := req
				select {
				case <-ctx.Done():
					return
				case out <- StreamEvent{Type: StreamEventInterruptRequired, Interrupt: &req}:
				}
			}
		}()
	}

	var finalResponse string
	var totalUsage TokenUsage
	messages := conv.Messages

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
			return err
		}

		var turnText strings.Builder
		var turnToolCalls []ToolCallEntry
		var turnDone bool

		for ev := range events {
			switch ev.Type {
			case StreamEventDelta:
				turnText.WriteString(ev.Delta)
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
				return ev.Error
			}
		}

		messages = append(messages, ChatMessage{
			Role:      RoleAssistant,
			Content:   turnText.String(),
			ToolCalls: turnToolCalls,
		})
		if turnText.Len() > 0 {
			finalResponse = turnText.String()
		}

		if len(turnToolCalls) == 0 || !turnDone {
			break
		}

		var allowedCalls []ToolCallEntry
		for _, call := range turnToolCalls {
			if r.safety != nil {
				verdict := r.safety.Inspect(ctx, call)
				if verdict.Decision == SafetyBlock {
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

		results := dispatcher.DispatchParallel(ctx, allowedCalls, "")
		for _, result := range results {
			out <- StreamEvent{Type: StreamEventToolResult, ToolResult: &result}
			messages = append(messages, ChatMessage{
				Role:       RoleTool,
				Content:    result.Content,
				ToolCallID: result.ToolCallID,
			})
		}
	}

	conv.AppendAssistant(finalResponse)

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
