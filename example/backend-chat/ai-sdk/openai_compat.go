package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// OpenAICompatProvider implements Provider for any OpenAI-compatible API.
// Works with: OpenAI, Groq, Together, OpenRouter, Mistral, DeepSeek, etc.
type OpenAICompatProvider struct {
	name       string
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewOpenAICompatProvider creates a provider from config.
func NewOpenAICompatProvider(cfg ProviderConfig) Provider {
	return &OpenAICompatProvider{
		name:    cfg.Name,
		baseURL: cfg.BaseURL,
		apiKey:  cfg.APIKey,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
	}
}

// Name returns the provider name.
func (p *OpenAICompatProvider) Name() string { return p.name }

// ── OpenAI API types (request) ───────────────────────────────────────────────

type openaiRequest struct {
	Model       string          `json:"model"`
	Messages    []openaiMessage `json:"messages"`
	Tools       []openaiTool    `json:"tools,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream"`
}

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openaiToolCallFunc `json:"function"`
}

type openaiToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

type openaiTool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ── OpenAI API types (response) ──────────────────────────────────────────────

type openaiResponse struct {
	ID      string         `json:"id"`
	Choices []openaiChoice `json:"choices"`
	Usage   openaiUsage    `json:"usage"`
}

type openaiChoice struct {
	Index        int           `json:"index"`
	Message      openaiMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ── Chat method ──────────────────────────────────────────────────────────────

// Chat sends a request to the OpenAI-compatible endpoint and translates
// the response back into our unified ChatResponse type.
func (p *OpenAICompatProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	// Convert internal types → OpenAI request format.
	oaiReq := openaiRequest{
		Model:    req.Model,
		Messages: toOpenAIMessages(req.Messages),
		Tools:    toOpenAITools(req.Tools),
		Stream:   false,
	}
	if req.Options != nil {
		oaiReq.Temperature = req.Options.Temperature
		if req.Options.NumCtx > 0 {
			oaiReq.MaxTokens = req.Options.NumCtx
		}
	}

	body, err := json.Marshal(oaiReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("%s: marshal request: %w", p.name, err)
	}

	url := p.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("%s: build request: %w", p.name, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	slog.Debug("openai-compat request", "provider", p.name, "model", req.Model, "messages", len(req.Messages))

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("%s: http call: %w", p.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return ChatResponse{}, fmt.Errorf("%s: status %d: %s", p.name, resp.StatusCode, string(errBody))
	}

	var oaiResp openaiResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return ChatResponse{}, fmt.Errorf("%s: decode response: %w", p.name, err)
	}

	if len(oaiResp.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("%s: empty choices in response", p.name)
	}

	// Convert OpenAI response → our unified ChatResponse.
	choice := oaiResp.Choices[0]
	chatResp := ChatResponse{
		Model:           req.Model,
		Message:         fromOpenAIMessage(choice.Message),
		Done:            true,
		EvalCount:       oaiResp.Usage.CompletionTokens,
		PromptEvalCount: oaiResp.Usage.PromptTokens,
	}

	slog.Debug("openai-compat response",
		"provider", p.name,
		"model", req.Model,
		"eval_count", chatResp.EvalCount,
		"prompt_eval_count", chatResp.PromptEvalCount,
		"tool_calls", len(chatResp.Message.ToolCalls),
	)

	return chatResp, nil
}

// ── Conversion helpers ───────────────────────────────────────────────────────

func toOpenAIMessages(msgs []ChatMessage) []openaiMessage {
	out := make([]openaiMessage, 0, len(msgs))
	for _, m := range msgs {
		om := openaiMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		// Convert tool calls from our format → OpenAI format.
		for i, tc := range m.ToolCalls {
			args, _ := json.Marshal(tc.Function.Arguments)
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			}
			om.ToolCalls = append(om.ToolCalls, openaiToolCall{
				ID:   id,
				Type: "function",
				Function: openaiToolCallFunc{
					Name:      tc.Function.Name,
					Arguments: string(args),
				},
			})
		}
		out = append(out, om)
	}
	return out
}

func toOpenAITools(defs []ToolDef) []openaiTool {
	if len(defs) == 0 {
		return nil
	}
	out := make([]openaiTool, 0, len(defs))
	for _, d := range defs {
		out = append(out, openaiTool{
			Type:     "function",
			Function: d.Function,
		})
	}
	return out
}

func fromOpenAIMessage(om openaiMessage) ChatMessage {
	cm := ChatMessage{
		Role:       om.Role,
		Content:    om.Content,
		ToolCallID: om.ToolCallID,
	}
	for _, tc := range om.ToolCalls {
		var args map[string]any
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		cm.ToolCalls = append(cm.ToolCalls, ToolCall{
			ID: tc.ID,
			Function: ToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: args,
			},
		})
	}
	return cm
}
