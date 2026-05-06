// Package llm provides production LLM providers for the autobuild SDK.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	autobuild "github.com/everfaz/autobuild-sdk"
)

// OpenAI implements autobuild.LLMProvider and StreamingLLMProvider against the
// OpenAI Chat Completions API. Compatible with OpenAI-spec endpoints:
// OpenAI, Groq, Together, OpenRouter, Mistral, DeepSeek, Anyscale, etc.
//
// Wire it into an Engine:
//
//	llmProvider := llm.NewOpenAI("https://api.openai.com/v1", os.Getenv("OPENAI_API_KEY"), "gpt-4o")
//	engine.LLM = llmProvider
//
// For OpenAI-compatible providers (Groq, Together, etc), pass the appropriate base URL.
type OpenAI struct {
	BaseURL      string
	APIKey       string
	DefaultModel string
	HTTPClient   *http.Client
}

// NewOpenAI creates a new OpenAI provider. baseURL must NOT have a trailing slash.
// Default to "https://api.openai.com/v1" for OpenAI itself.
func NewOpenAI(baseURL, apiKey, defaultModel string) *OpenAI {
	return &OpenAI{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		APIKey:       apiKey,
		DefaultModel: defaultModel,
		HTTPClient:   &http.Client{Timeout: 10 * time.Minute},
	}
}

// ── Chat (blocking) ──────────────────────────────────────────────────────────

func (o *OpenAI) Chat(ctx context.Context, req autobuild.ChatRequest) (*autobuild.ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = o.DefaultModel
	}
	if model == "" {
		return nil, fmt.Errorf("openai: Model is required")
	}

	body, err := buildOpenAIRequest(model, req, false)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		o.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}

	resp, err := o.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: %d: %s", resp.StatusCode, string(errBody))
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: read body: %w", err)
	}
	return parseOpenAIResponse(respBody)
}

// ── ChatStream ───────────────────────────────────────────────────────────────

func (o *OpenAI) ChatStream(ctx context.Context, req autobuild.ChatRequest) (<-chan autobuild.StreamEvent, error) {
	model := req.Model
	if model == "" {
		model = o.DefaultModel
	}
	if model == "" {
		return nil, fmt.Errorf("openai: Model is required")
	}

	body, err := buildOpenAIRequest(model, req, true)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		o.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}

	resp, err := o.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: http: %w", err)
	}

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("openai: %d: %s", resp.StatusCode, string(errBody))
	}

	out := make(chan autobuild.StreamEvent, 32)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		readOpenAISSE(ctx, resp.Body, out)
	}()
	return out, nil
}

// ── Request building ─────────────────────────────────────────────────────────

func buildOpenAIRequest(model string, req autobuild.ChatRequest, stream bool) ([]byte, error) {
	msgs := make([]map[string]any, 0, len(req.Messages))
	for i, m := range req.Messages {
		// Build content — string for plain, array for multimodal (text + images)
		var content any = m.Content
		if len(m.Images) > 0 {
			parts := make([]map[string]any, 0, len(m.Images)+1)
			if m.Content != "" {
				parts = append(parts, map[string]any{
					"type": "text",
					"text": m.Content,
				})
			}
			for _, img := range m.Images {
				url := img.URL
				if url == "" && img.Source != "" {
					mt := img.MediaType
					if mt == "" {
						mt = "image/jpeg"
					}
					url = "data:" + mt + ";base64," + img.Source
				}
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": url},
				})
			}
			content = parts
		}
		msg := map[string]any{
			"role":    string(m.Role),
			"content": content,
		}
		if m.Name != "" && m.Role == autobuild.RoleTool {
			msg["name"] = m.Name
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		if len(m.ToolCalls) > 0 {
			tcs := make([]map[string]any, 0, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				id := tc.ID
				if id == "" {
					id = fmt.Sprintf("call_%d_%d", i, j)
				}
				tcs = append(tcs, map[string]any{
					"id":   id,
					"type": "function",
					"function": map[string]any{
						"name":      tc.Name,
						"arguments": tc.Arguments,
					},
				})
			}
			msg["tool_calls"] = tcs
		}
		msgs = append(msgs, msg)
	}

	body := map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   stream,
	}
	if stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	if req.ReasoningEffort != "" {
		body["reasoning_effort"] = req.ReasoningEffort
	}
	if len(req.Stop) > 0 {
		body["stop"] = req.Stop
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type":     "function",
				"function": t.Function,
			})
		}
		body["tools"] = tools
	}

	return json.Marshal(body)
}

// ── Response parsing (blocking) ──────────────────────────────────────────────

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Role      string             `json:"role"`
			Content   string             `json:"content"`
			ToolCalls []openAIToolCall   `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func parseOpenAIResponse(body []byte) (*autobuild.ChatResponse, error) {
	var raw openAIResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("openai: decode: %w", err)
	}
	if len(raw.Choices) == 0 {
		return nil, fmt.Errorf("openai: empty choices")
	}
	choice := raw.Choices[0]
	out := &autobuild.ChatResponse{
		Content:      choice.Message.Content,
		FinishReason: mapOpenAIFinishReason(choice.FinishReason),
		Model:        raw.Model,
		Usage: autobuild.TokenUsage{
			PromptTokens:     raw.Usage.PromptTokens,
			CompletionTokens: raw.Usage.CompletionTokens,
			TotalTokens:      raw.Usage.TotalTokens,
		},
	}
	for _, tc := range choice.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, autobuild.ToolCallEntry{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return out, nil
}

// ── SSE streaming ────────────────────────────────────────────────────────────

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

func readOpenAISSE(ctx context.Context, body io.Reader, out chan<- autobuild.StreamEvent) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	// Tool calls stream as fragments by index — accumulate until finish_reason
	type pending struct {
		ID   string
		Name string
		Args strings.Builder
	}
	toolBuf := make(map[int]*pending)
	finalUsage := autobuild.TokenUsage{}

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			out <- autobuild.StreamEvent{Type: autobuild.StreamEventError, Error: ctx.Err()}
			return
		default:
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // skip malformed lines
		}
		if chunk.Usage != nil {
			finalUsage.PromptTokens = chunk.Usage.PromptTokens
			finalUsage.CompletionTokens = chunk.Usage.CompletionTokens
			finalUsage.TotalTokens = chunk.Usage.TotalTokens
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]

		// Text delta
		if choice.Delta.Content != "" {
			out <- autobuild.StreamEvent{
				Type:  autobuild.StreamEventDelta,
				Delta: choice.Delta.Content,
			}
		}

		// Tool call deltas — accumulate by index
		for _, tc := range choice.Delta.ToolCalls {
			p, ok := toolBuf[tc.Index]
			if !ok {
				p = &pending{}
				toolBuf[tc.Index] = p
			}
			if tc.ID != "" {
				p.ID = tc.ID
			}
			if tc.Function.Name != "" {
				p.Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				p.Args.WriteString(tc.Function.Arguments)
			}
		}

		// On finish_reason="tool_calls", emit accumulated tool calls
		if choice.FinishReason != "" {
			for _, p := range toolBuf {
				if p.Name != "" {
					call := &autobuild.ToolCallEntry{
						ID:        p.ID,
						Name:      p.Name,
						Arguments: p.Args.String(),
					}
					out <- autobuild.StreamEvent{
						Type:     autobuild.StreamEventToolCall,
						ToolCall: call,
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		out <- autobuild.StreamEvent{
			Type:  autobuild.StreamEventError,
			Error: fmt.Errorf("openai stream read: %w", err),
		}
		return
	}

	out <- autobuild.StreamEvent{
		Type: autobuild.StreamEventDone,
		Final: &autobuild.AgentLoopResult{
			TotalUsage: finalUsage,
		},
	}
}

func mapOpenAIFinishReason(s string) string {
	switch s {
	case "stop":
		return "stop"
	case "tool_calls", "function_call":
		return "tool_calls"
	case "length":
		return "length"
	case "content_filter":
		return "content_filter"
	default:
		return s
	}
}

// Verify interface implementations
var (
	_ autobuild.LLMProvider          = (*OpenAI)(nil)
	_ autobuild.StreamingLLMProvider = (*OpenAI)(nil)
)
