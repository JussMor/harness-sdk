// Package llm provides production LLMProvider implementations for the
// autobuild SDK. Each backend lives in its own file so importing one
// doesn't pull in the dependencies of others.
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

// Anthropic is an LLMProvider backed by api.anthropic.com.
//
// Wire it into an Engine:
//
//	engine.LLM = llm.NewAnthropic("sk-ant-...", "claude-sonnet-4-20250514")
//
// This implementation supports:
//   - Chat completions with tool use
//   - Multi-turn conversations
//   - System prompts
//   - Custom timeouts via Client field
//
// It does NOT yet support:
//   - Streaming (use a future StreamingAnthropic wrapper)
//   - Vision/image inputs (text only)
//   - Prompt caching headers (set manually if needed)
type Anthropic struct {
	APIKey       string
	DefaultModel string

	// BaseURL defaults to "https://api.anthropic.com/v1/messages".
	// Override for proxies or compatible endpoints (e.g. AWS Bedrock).
	BaseURL string

	// AnthropicVersion is the API version header. Defaults to "2023-06-01".
	AnthropicVersion string

	// MaxTokens caps the response. Defaults to 4096.
	MaxTokens int

	// Client is the HTTP client used for requests. Defaults to one with 90s timeout.
	Client *http.Client
}

// NewAnthropic creates a default Anthropic provider with sensible defaults.
func NewAnthropic(apiKey, defaultModel string) *Anthropic {
	return &Anthropic{
		APIKey:           apiKey,
		DefaultModel:     defaultModel,
		BaseURL:          "https://api.anthropic.com/v1/messages",
		AnthropicVersion: "2023-06-01",
		MaxTokens:        4096,
		Client:           &http.Client{Timeout: 90 * time.Second},
	}
}

// Chat implements autobuild.LLMProvider.
func (a *Anthropic) Chat(ctx context.Context, req autobuild.ChatRequest) (*autobuild.ChatResponse, error) {
	if a.APIKey == "" {
		return nil, fmt.Errorf("anthropic: APIKey is required")
	}

	model := req.Model
	if model == "" {
		model = a.DefaultModel
	}
	if model == "" {
		return nil, fmt.Errorf("anthropic: model not specified")
	}

	maxTokens := a.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	// Build wire format
	body, err := buildAnthropicRequest(model, maxTokens, req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.APIKey)
	httpReq.Header.Set("anthropic-version", a.AnthropicVersion)

	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		// Surface enough info for callWithRetry to classify (rate limit, auth, etc.)
		return nil, fmt.Errorf("anthropic: %d %s: %s", resp.StatusCode, resp.Status, string(respBody))
	}

	return parseAnthropicResponse(respBody)
}

// ── Wire format helpers ──────────────────────────────────────────────────────

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ID       string          `json:"id,omitempty"`        // tool_use
	Name     string          `json:"name,omitempty"`      // tool_use
	Input    json.RawMessage `json:"input,omitempty"`     // tool_use
	ToolUseID string         `json:"tool_use_id,omitempty"` // tool_result
	Content  string          `json:"content,omitempty"`   // tool_result body
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Role       string             `json:"role"`
	Content    []anthropicContent `json:"content"`
	Model      string             `json:"model"`
	StopReason string             `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func buildAnthropicRequest(model string, maxTokens int, req autobuild.ChatRequest) ([]byte, error) {
	out := anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
	}

	for _, m := range req.Messages {
		switch m.Role {
		case autobuild.RoleSystem:
			// Anthropic uses a top-level system field, not a message role.
			if out.System != "" {
				out.System += "\n\n" + m.Content
			} else {
				out.System = m.Content
			}
		case autobuild.RoleUser, autobuild.RoleAssistant:
			out.Messages = append(out.Messages, anthropicMessage{
				Role: string(m.Role),
				Content: []anthropicContent{
					{Type: "text", Text: m.Content},
				},
			})
		case autobuild.RoleTool:
			// Tool results land in a user message with a tool_result block.
			out.Messages = append(out.Messages, anthropicMessage{
				Role: "user",
				Content: []anthropicContent{
					{
						Type:      "tool_result",
						ToolUseID: m.ToolCallID,
						Content:   m.Content,
					},
				},
			})
		}
	}

	// Tools
	for _, t := range req.Tools {
		schema, err := json.Marshal(t.Function.Parameters)
		if err != nil {
			return nil, fmt.Errorf("marshal tool %q schema: %w", t.Function.Name, err)
		}
		out.Tools = append(out.Tools, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: schema,
		})
	}

	return json.Marshal(out)
}

func parseAnthropicResponse(body []byte) (*autobuild.ChatResponse, error) {
	var raw anthropicResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	out := &autobuild.ChatResponse{
		Model:        raw.Model,
		FinishReason: mapAnthropicStopReason(raw.StopReason),
		Usage: autobuild.TokenUsage{
			PromptTokens:     raw.Usage.InputTokens,
			CompletionTokens: raw.Usage.OutputTokens,
			TotalTokens:      raw.Usage.InputTokens + raw.Usage.OutputTokens,
		},
	}

	for _, c := range raw.Content {
		switch c.Type {
		case "text":
			if out.Content != "" {
				out.Content += "\n"
			}
			out.Content += c.Text
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, autobuild.ToolCallEntry{
				ID:        c.ID,
				Name:      c.Name,
				Arguments: string(c.Input),
			})
		}
	}

	return out, nil
}

// Verify Anthropic implements the SDK interface.
var _ autobuild.LLMProvider = (*Anthropic)(nil)

// mapAnthropicStopReason maps Anthropic-specific stop_reason values to the
// SDK's standardized FinishReason vocabulary.
func mapAnthropicStopReason(s string) string {
	switch s {
	case "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	default:
		return s
	}
}
