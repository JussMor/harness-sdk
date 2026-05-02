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

// OllamaClient talks to a local Ollama instance via its HTTP API.
// It implements the Provider interface.
type OllamaClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewOllamaClient returns a client pointing at the given Ollama URL.
func NewOllamaClient(baseURL string) *OllamaClient {
	return &OllamaClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Minute, // LLM calls can be slow
		},
	}
}

// NewOllamaProvider creates an OllamaClient from a ProviderConfig.
func NewOllamaProvider(cfg ProviderConfig) Provider {
	return NewOllamaClient(cfg.BaseURL)
}

// Name returns "ollama".
func (o *OllamaClient) Name() string { return "ollama" }

// ── Ollama API types ─────────────────────────────────────────────────────────

// ChatRequest is the payload for POST /api/chat.
type ChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Tools    []ToolDef     `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`
	Options  *ModelOptions `json:"options,omitempty"`
}

// ModelOptions controls generation parameters.
type ModelOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumCtx      int     `json:"num_ctx,omitempty"`
}

// ChatMessage represents a single message in the conversation.
type ChatMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID is set on role="tool" messages, linking the result to the
	// originating assistant tool_use block. Required by Anthropic and the
	// OpenAI tools API for multi-turn conversations.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCall is a function call requested by the model.
type ToolCall struct {
	// ID is the provider-assigned tool_use ID (Anthropic) or tool_call ID
	// (OpenAI). Empty for providers that don't expose one (e.g. Ollama).
	ID       string           `json:"id,omitempty"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction is the name + arguments of a tool invocation.
type ToolCallFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ToolDef describes a tool the model can call (OpenAI-compatible schema).
type ToolDef struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the schema for a single tool.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  ToolFuncParams `json:"parameters"`
}

// ToolFuncParams is a JSON-Schema-style object describing the tool's args.
type ToolFuncParams struct {
	Type       string               `json:"type"`
	Properties map[string]ToolParam `json:"properties"`
	Required   []string             `json:"required"`
}

// ToolParam describes a single parameter.
type ToolParam struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// ChatResponse is the Ollama reply for a non-streaming chat call.
type ChatResponse struct {
	Model     string      `json:"model"`
	Message   ChatMessage `json:"message"`
	Done      bool        `json:"done"`
	Reasoning string      `json:"reasoning,omitempty"`

	// Token accounting fields.
	TotalDuration   int64 `json:"total_duration"`
	EvalCount       int   `json:"eval_count"`
	PromptEvalCount int   `json:"prompt_eval_count"`
}

// ── Client method ────────────────────────────────────────────────────────────

// Chat sends a non-streaming chat completion request to Ollama and returns the
// assistant response. The caller is responsible for building the messages list
// (system prompt, conversation history, tool results, etc.).
func (o *OllamaClient) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	req.Stream = false

	body, err := json.Marshal(req)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("ollama: marshal request: %w", err)
	}

	url := o.BaseURL + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("ollama: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	slog.Debug("ollama request", "model", req.Model, "messages", len(req.Messages))

	resp, err := o.HTTPClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("ollama: http call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return ChatResponse{}, fmt.Errorf("ollama: status %d: %s", resp.StatusCode, string(errBody))
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return ChatResponse{}, fmt.Errorf("ollama: decode response: %w", err)
	}

	slog.Debug("ollama response",
		"model", chatResp.Model,
		"eval_count", chatResp.EvalCount,
		"prompt_eval_count", chatResp.PromptEvalCount,
		"tool_calls", len(chatResp.Message.ToolCalls),
	)

	return chatResp, nil
}
