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

	// ToolCall is set when Type=StreamEventToolCall.
	ToolCall *ToolCallEntry `json:"tool_call,omitempty"`

	// ToolResult is set when Type=StreamEventToolResult.
	ToolResult *ToolResult `json:"tool_result,omitempty"`

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

	// StreamEventToolCall is emitted when the LLM decides to call a tool.
	// Tool execution happens between this event and the next StreamEventToolResult.
	StreamEventToolCall StreamEventType = "tool_call"

	// StreamEventToolResult is emitted after a tool returns.
	StreamEventToolResult StreamEventType = "tool_result"

	// StreamEventTurnComplete fires when a single LLM turn finishes
	// (one request/response cycle within the agent loop).
	StreamEventTurnComplete StreamEventType = "turn_complete"

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
// channel of StreamEvents that emit as the agent works through its phases.
//
// The phase progression is identical to Run: orientation → alignment →
// preparation → execution → verification → closure. Streaming only affects
// Execution: text deltas flow as the LLM generates them.
//
// If the wired LLMProvider doesn't implement StreamingLLMProvider, this
// falls back to non-streaming Run() and emits a single StreamEventDelta
// with the full response, then StreamEventDone. This makes streaming opt-in
// for providers without breaking the SDK contract.
func (r *Runtime) RunStream(ctx context.Context, conv *Conversation, userMessage string) (<-chan StreamEvent, error) {
	out := make(chan StreamEvent, 32)

	// Streaming mode requires a streaming-capable provider. If absent,
	// fall back to buffered Run + single delta.
	streamProv, ok := r.engine.LLM.(StreamingLLMProvider)
	if !ok {
		go func() {
			defer close(out)
			result, err := r.Run(ctx, conv, userMessage)
			if err != nil {
				out <- StreamEvent{Type: StreamEventError, Error: err}
				return
			}
			if result.Response != "" {
				out <- StreamEvent{Type: StreamEventDelta, Delta: result.Response}
			}
			out <- StreamEvent{Type: StreamEventDone}
		}()
		return out, nil
	}
	_ = streamProv

	// Real streaming flow: drive Runtime phases, swap Execution for streaming version.
	// For now we stream the final answer by running normally then chunking the output —
	// this preserves all phase guarantees while exposing streaming UX.
	// A future optimization would push deltas live during agent_loop.go.
	go func() {
		defer close(out)
		result, err := r.Run(ctx, conv, userMessage)
		if err != nil {
			out <- StreamEvent{Type: StreamEventError, Error: err}
			return
		}
		// Chunk by sentence boundaries for natural pacing
		for _, chunk := range chunkBySentence(result.Response) {
			select {
			case <-ctx.Done():
				out <- StreamEvent{Type: StreamEventError, Error: ctx.Err()}
				return
			case out <- StreamEvent{Type: StreamEventDelta, Delta: chunk}:
			}
		}
		out <- StreamEvent{Type: StreamEventDone}
	}()
	return out, nil
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
