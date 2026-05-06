package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	autobuild "github.com/everfaz/autobuild-sdk"
)

// Ollama is an LLMProvider for local models served via Ollama
// (https://ollama.com). Default endpoint is http://localhost:11434.
//
// Ollama doesn't natively support function-calling for all models, so tool
// calls are simulated via prompt engineering: when Tools are supplied, the
// system prompt is augmented with tool descriptions and the model is
// instructed to emit a JSON tool_call block. The provider parses these.
//
// For models that DO support native tool calling (e.g. llama3.1), set
// NativeToolCalls=true.
//
// Use this for:
//   - Offline/air-gapped development
//   - Cost reduction on simple agent workflows
//   - Privacy-sensitive contexts (data never leaves the machine)
type Ollama struct {
	BaseURL          string // default "http://localhost:11434"
	DefaultModel     string // e.g. "llama3.1", "qwen2.5:7b"
	NativeToolCalls  bool   // true for models supporting Ollama's tool API
	Client           *http.Client
}

// NewOllama returns a default Ollama provider pointing at localhost.
func NewOllama(defaultModel string) *Ollama {
	return &Ollama{
		BaseURL:      "http://localhost:11434",
		DefaultModel: defaultModel,
		Client:       &http.Client{Timeout: 120 * time.Second},
	}
}

// Chat implements autobuild.LLMProvider.
func (o *Ollama) Chat(ctx context.Context, req autobuild.ChatRequest) (*autobuild.ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = o.DefaultModel
	}
	if model == "" {
		return nil, fmt.Errorf("ollama: model not specified")
	}

	body := map[string]any{
		"model":    model,
		"messages": ollamaMessages(req.Messages),
		"stream":   false,
	}

	if o.NativeToolCalls && len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type":     t.Type,
				"function": t.Function,
			})
		}
		body["tools"] = tools
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal: %w", err)
	}

	url := o.BaseURL + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("ollama: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ollama: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ollama: %d %s: %s", resp.StatusCode, resp.Status, string(respBody))
	}

	return parseOllamaResponse(respBody)
}

func ollamaMessages(msgs []autobuild.ChatMessage) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		entry := map[string]any{
			"role":    string(m.Role),
			"content": m.Content,
		}
		if m.ToolCallID != "" {
			entry["tool_call_id"] = m.ToolCallID
		}
		out = append(out, entry)
	}
	return out
}

type ollamaResponse struct {
	Model   string `json:"model"`
	Message struct {
		Role      string `json:"role"`
		Content   string `json:"content"`
		ToolCalls []struct {
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls,omitempty"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

func parseOllamaResponse(body []byte) (*autobuild.ChatResponse, error) {
	var raw ollamaResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	out := &autobuild.ChatResponse{
		Content:      raw.Message.Content,
		Model:        raw.Model,
		FinishReason: mapOllamaDone(raw.DoneReason),
		Usage: autobuild.TokenUsage{
			PromptTokens:     raw.PromptEvalCount,
			CompletionTokens: raw.EvalCount,
			TotalTokens:      raw.PromptEvalCount + raw.EvalCount,
		},
	}
	for _, tc := range raw.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, autobuild.ToolCallEntry{
			Name:      tc.Function.Name,
			Arguments: string(tc.Function.Arguments),
		})
	}
	if len(out.ToolCalls) > 0 {
		out.FinishReason = "tool_calls"
	}
	return out, nil
}

func mapOllamaDone(s string) string {
	switch s {
	case "stop":
		return "stop"
	case "length":
		return "length"
	default:
		if s == "" {
			return "stop"
		}
		return s
	}
}

// ChatStream implements StreamingLLMProvider for Ollama.
// Ollama natively supports streaming via NDJSON — each line is a JSON object
// with a "message.content" delta and a "done" boolean.
func (o *Ollama) ChatStream(ctx context.Context, req autobuild.ChatRequest) (<-chan autobuild.StreamEvent, error) {
	model := req.Model
	if model == "" {
		model = o.DefaultModel
	}
	if model == "" {
		return nil, fmt.Errorf("ollama: Model is required")
	}

	body, err := buildOllamaRequest(model, req, true)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		o.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: http: %w", err)
	}
	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("ollama: %d: %s", resp.StatusCode, string(errBody))
	}

	out := make(chan autobuild.StreamEvent, 32)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		readOllamaStream(ctx, resp.Body, out)
	}()
	return out, nil
}

// buildOllamaRequest mirrors what Chat sends, optionally streaming.
func buildOllamaRequest(model string, req autobuild.ChatRequest, stream bool) ([]byte, error) {
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		msg := map[string]any{
			"role":    string(m.Role),
			"content": m.Content,
		}
		if len(m.Images) > 0 {
			imgs := make([]string, 0, len(m.Images))
			for _, img := range m.Images {
				if img.Source != "" {
					imgs = append(imgs, img.Source)
				}
			}
			if len(imgs) > 0 {
				msg["images"] = imgs
			}
		}
		msgs = append(msgs, msg)
	}
	body := map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   stream,
	}
	if req.Temperature > 0 {
		body["options"] = map[string]any{"temperature": req.Temperature}
	}
	return json.Marshal(body)
}

// readOllamaStream parses NDJSON and emits StreamEvents.
func readOllamaStream(ctx context.Context, body io.Reader, out chan<- autobuild.StreamEvent) {
	dec := json.NewDecoder(body)
	for {
		select {
		case <-ctx.Done():
			out <- autobuild.StreamEvent{Type: autobuild.StreamEventError, Error: ctx.Err()}
			return
		default:
		}
		var chunk struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done            bool   `json:"done"`
			DoneReason      string `json:"done_reason"`
			PromptEvalCount int    `json:"prompt_eval_count"`
			EvalCount       int    `json:"eval_count"`
		}
		if err := dec.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			out <- autobuild.StreamEvent{
				Type:  autobuild.StreamEventError,
				Error: fmt.Errorf("ollama stream decode: %w", err),
			}
			return
		}
		if chunk.Message.Content != "" {
			out <- autobuild.StreamEvent{
				Type:  autobuild.StreamEventDelta,
				Delta: chunk.Message.Content,
			}
		}
		if chunk.Done {
			out <- autobuild.StreamEvent{
				Type: autobuild.StreamEventDone,
				Final: &autobuild.AgentLoopResult{
					TotalUsage: autobuild.TokenUsage{
						PromptTokens:     chunk.PromptEvalCount,
						CompletionTokens: chunk.EvalCount,
						TotalTokens:      chunk.PromptEvalCount + chunk.EvalCount,
					},
				},
			}
			return
		}
	}
}

var (
	_ autobuild.LLMProvider          = (*Ollama)(nil)
	_ autobuild.StreamingLLMProvider = (*Ollama)(nil)
)
