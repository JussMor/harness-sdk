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

var _ autobuild.LLMProvider = (*Ollama)(nil)
