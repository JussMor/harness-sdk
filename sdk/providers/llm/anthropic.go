// Package llm provides production LLMProvider implementations for the
// autobuild SDK. Each backend lives in its own file so importing one
// doesn't pull in the dependencies of others.
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
	Type         string                 `json:"type"`
	Text         *string                `json:"text,omitempty"`
	Thinking     *string                `json:"thinking,omitempty"`  // extended thinking block
	ID           string                 `json:"id,omitempty"`          // tool_use
	Name         string                 `json:"name,omitempty"`        // tool_use
	Input        json.RawMessage        `json:"input,omitempty"`       // tool_use
	ToolUseID    string                 `json:"tool_use_id,omitempty"` // tool_result
	Content      string                 `json:"content,omitempty"`     // tool_result body
	Source       *anthropicImageSource  `json:"source,omitempty"`      // image
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"` // message-level caching
}

// anthropicImageSource represents the image data — either base64 or url.
type anthropicImageSource struct {
	Type      string `json:"type"`                 // "base64" or "url"
	MediaType string `json:"media_type,omitempty"` // "image/jpeg", "image/png", etc.
	Data      string `json:"data,omitempty"`       // base64 data (for type=base64)
	URL       string `json:"url,omitempty"`        // image URL (for type=url)
}

// buildAnthropicImageBlock converts an SDK ImageContent to an Anthropic content block.
func buildAnthropicImageBlock(img autobuild.ImageContent) anthropicContent {
	src := &anthropicImageSource{}
	if img.URL != "" {
		src.Type = "url"
		src.URL = img.URL
	} else {
		src.Type = "base64"
		src.MediaType = img.MediaType
		src.Data = img.Source
	}
	return anthropicContent{
		Type:   "image",
		Source: src,
	}
}

func strPtr(s string) *string { return &s }

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicRequest struct {
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	System       string                 `json:"-"` // use SystemBlocks when set
	SystemBlocks []anthropicSystemBlock `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
}

// anthropicThinking enables extended thinking mode.
// Only supported by Claude 3.7+ models.
// BudgetTokens must be at least 1024 and less than MaxTokens.
type anthropicThinking struct {
	Type         string `json:"type"`          // always "enabled"
	BudgetTokens int    `json:"budget_tokens"` // how many tokens the model can spend thinking
}

type anthropicSystemBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicCacheControl struct {
	Type string `json:"type"` // "ephemeral"
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

	// Extended thinking: when ThinkingBudget is set, enable the thinking block.
	// MaxTokens must exceed ThinkingBudget — enforce minimum here.
	if req.ThinkingBudget > 0 {
		budget := req.ThinkingBudget
		if budget < 1024 {
			budget = 1024 // Anthropic minimum
		}
		if out.MaxTokens <= budget {
			out.MaxTokens = budget + 4096 // ensure room for response
		}
		out.Thinking = &anthropicThinking{
			Type:         "enabled",
			BudgetTokens: budget,
		}
	}

	// Prompt caching: mark the system prompt for caching so Anthropic can
	// reuse it across turns without re-processing. This reduces latency and
	// cost significantly for long system prompts (skills, memory, behavior).
	systemContent := ""
	for _, m := range req.Messages {
		if m.Role == autobuild.RoleSystem {
			if systemContent != "" {
				systemContent += "\n\n" + m.Content
			} else {
				systemContent = m.Content
			}
		}
	}
	if systemContent != "" {
		out.System = systemContent
		// Mark for caching — Anthropic will cache this if ≥ 1024 tokens
		out.SystemBlocks = []anthropicSystemBlock{
			{Type: "text", Text: systemContent, CacheControl: &anthropicCacheControl{Type: "ephemeral"}},
		}
	}

	// Convert messages — critical: batch consecutive RoleTool messages into
	// a single user message with multiple tool_result blocks.
	// Anthropic rejects requests where tool_results are in separate user messages.
	i := 0
	msgs := req.Messages
	for i < len(msgs) {
		m := msgs[i]
		switch m.Role {
		case autobuild.RoleSystem:
			i++
			continue

		case autobuild.RoleUser:
			// Build content blocks: images first, then text
			var content []anthropicContent
			for _, img := range m.Images {
				content = append(content, buildAnthropicImageBlock(img))
			}
			if m.Content != "" {
				content = append(content, anthropicContent{Type: "text", Text: strPtr(m.Content)})
			}
			if len(content) > 0 {
				out.Messages = append(out.Messages, anthropicMessage{
					Role:    "user",
					Content: content,
				})
			}
			i++

		case autobuild.RoleAssistant:
			var content []anthropicContent
			if m.Content != "" {
				content = append(content, anthropicContent{Type: "text", Text: strPtr(m.Content)})
			}
			for _, tc := range m.ToolCalls {
				input := json.RawMessage(tc.Arguments)
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				content = append(content, anthropicContent{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: input,
				})
			}
			if len(content) == 0 {
				content = append(content, anthropicContent{Type: "text", Text: strPtr("")})
			}
			out.Messages = append(out.Messages, anthropicMessage{
				Role:    "assistant",
				Content: content,
			})
			i++

		case autobuild.RoleTool:
			// Batch ALL consecutive tool results into one user message.
			// This is required by the Anthropic API — separate user messages
			// for each tool result cause a 400 error.
			var toolResults []anthropicContent
			for i < len(msgs) && msgs[i].Role == autobuild.RoleTool {
				toolResults = append(toolResults, anthropicContent{
					Type:      "tool_result",
					ToolUseID: msgs[i].ToolCallID,
					Content:   msgs[i].Content,
				})
				i++
			}
			out.Messages = append(out.Messages, anthropicMessage{
				Role:    "user",
				Content: toolResults,
			})

		default:
			i++
		}
	}

	// Message-level prompt caching: mark the last assistant or tool_result message
	// for caching so subsequent turns can skip re-processing the conversation history.
	// Anthropic caches up to 4 cache breakpoints; we use 1 here (system + 1 message).
	// Best location: the final block of the last assistant or tool_result message,
	// because the user's next turn will arrive after it and the cache persists.
	if len(out.Messages) >= 2 {
		// Find the last message that is "stable" (not the latest user message)
		// — typically the assistant turn before the new user input.
		for idx := len(out.Messages) - 1; idx >= 0; idx-- {
			msg := &out.Messages[idx]
			if msg.Role == "assistant" || (msg.Role == "user" && hasToolResult(msg.Content)) {
				if len(msg.Content) > 0 {
					msg.Content[len(msg.Content)-1].CacheControl = &anthropicCacheControl{Type: "ephemeral"}
				}
				break
			}
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

// hasToolResult returns true if the content includes a tool_result block,
// which means the message is a user message echoing tool outputs (good cache point).
func hasToolResult(content []anthropicContent) bool {
	for _, c := range content {
		if c.Type == "tool_result" {
			return true
		}
	}
	return false
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
		case "thinking":
			// Extended thinking content — internal model reasoning.
			// Stored separately from Content, not shown to users by default.
			if c.Thinking != nil {
				if out.ThinkingContent != "" {
					out.ThinkingContent += "\n"
				}
				out.ThinkingContent += *c.Thinking
			}
		case "text":
			if c.Text != nil {
				if out.Content != "" {
					out.Content += "\n"
				}
				out.Content += *c.Text
			}
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

// Verify Anthropic implements StreamingLLMProvider.
var _ autobuild.StreamingLLMProvider = (*Anthropic)(nil)

// ChatStream implements autobuild.StreamingLLMProvider.
// It calls the Anthropic API with stream=true and emits token-by-token
// StreamEvents as the model generates them.
//
// The channel closes after StreamEventDone (success) or StreamEventError (failure).
// Cancel via ctx to abort mid-stream — the HTTP connection is closed promptly.
func (a *Anthropic) ChatStream(ctx context.Context, req autobuild.ChatRequest) (<-chan autobuild.StreamEvent, error) {
	if a.APIKey == "" {
		return nil, fmt.Errorf("anthropic: APIKey is required")
	}
	model := req.Model
	if model == "" {
		model = a.DefaultModel
	}

	maxTokens := a.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	body, err := buildAnthropicRequest(model, maxTokens, req)
	if err != nil {
		return nil, fmt.Errorf("anthropic stream: build request: %w", err)
	}

	// Inject stream=true into the request body
	var rawBody map[string]any
	if err := json.Unmarshal(body, &rawBody); err != nil {
		return nil, fmt.Errorf("anthropic stream: inject stream flag: %w", err)
	}
	rawBody["stream"] = true
	body, err = json.Marshal(rawBody)
	if err != nil {
		return nil, fmt.Errorf("anthropic stream: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic stream: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.APIKey)
	httpReq.Header.Set("anthropic-version", a.AnthropicVersion)
	httpReq.Header.Set("Accept", "text/event-stream")

	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 0} // no timeout for streaming — use ctx
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic stream: http: %w", err)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic stream: %d %s: %s", resp.StatusCode, resp.Status, string(body))
	}

	out := make(chan autobuild.StreamEvent, 64)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		readAnthropicSSE(ctx, resp.Body, out)
	}()
	return out, nil
}

// readAnthropicSSE parses the SSE stream from Anthropic and emits StreamEvents.
// Anthropic SSE format: lines starting with "data: " containing JSON objects.
// Events flow: message_start → content_block_start → content_block_delta* →
//              content_block_stop → message_delta → message_stop
func readAnthropicSSE(ctx context.Context, body io.Reader, out chan<- autobuild.StreamEvent) {
	scanner := newLineScanner(body)

	// Accumulated state across the stream
	var (
		inputTokens  int
		outputTokens int
		model        string
		toolCalls    []autobuild.ToolCallEntry
		currentTool  *autobuild.ToolCallEntry
		toolArgsBuf  strings.Builder
	)

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

		var event sseEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue // skip malformed lines
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				model = event.Message.Model
				inputTokens = event.Message.Usage.InputTokens
			}

		case "content_block_start":
			if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
				currentTool = &autobuild.ToolCallEntry{
					ID:   event.ContentBlock.ID,
					Name: event.ContentBlock.Name,
				}
				toolArgsBuf.Reset()
			}

		case "content_block_delta":
			if event.Delta == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				if event.Delta.Text != "" {
					out <- autobuild.StreamEvent{
						Type:  autobuild.StreamEventDelta,
						Delta: event.Delta.Text,
					}
				}
			case "thinking_delta":
				// Extended thinking — internal model reasoning streamed in real time.
				if event.Delta.Thinking != "" {
					out <- autobuild.StreamEvent{
						Type:     autobuild.StreamEventThinking,
						Thinking: event.Delta.Thinking,
					}
				}
			case "input_json_delta":
				// Accumulate tool call arguments
				toolArgsBuf.WriteString(event.Delta.PartialJSON)
			}

		case "content_block_stop":
			if currentTool != nil {
				currentTool.Arguments = toolArgsBuf.String()
				toolCalls = append(toolCalls, *currentTool)
				out <- autobuild.StreamEvent{
					Type:     autobuild.StreamEventToolCall,
					ToolCall: currentTool,
				}
				currentTool = nil
				toolArgsBuf.Reset()
			}

		case "message_delta":
			if event.Usage != nil {
				outputTokens = event.Usage.OutputTokens
			}

		case "message_stop":
			finalResult := &autobuild.AgentLoopResult{
				TotalUsage: autobuild.TokenUsage{
					PromptTokens:     inputTokens,
					CompletionTokens: outputTokens,
					TotalTokens:      inputTokens + outputTokens,
				},
			}
			_ = model
			_ = toolCalls
			out <- autobuild.StreamEvent{
				Type:  autobuild.StreamEventDone,
				Final: finalResult,
			}
			return

		case "error":
			out <- autobuild.StreamEvent{
				Type:  autobuild.StreamEventError,
				Error: fmt.Errorf("anthropic stream error: %s", data),
			}
			return
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		out <- autobuild.StreamEvent{
			Type:  autobuild.StreamEventError,
			Error: fmt.Errorf("anthropic stream read: %w", err),
		}
	}
}

// ── SSE types ────────────────────────────────────────────────────────────────

type sseEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index,omitempty"`

	// message_start
	Message *struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	} `json:"message,omitempty"`

	// content_block_start
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block,omitempty"`

	// content_block_delta
	Delta *struct {
		Type         string `json:"type"`
		Text         string `json:"text,omitempty"`
		Thinking     string `json:"thinking,omitempty"`      // thinking_delta
		PartialJSON  string `json:"partial_json,omitempty"`
	} `json:"delta,omitempty"`

	// message_delta
	Usage *struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

// newLineScanner returns a bufio.Scanner that reads lines from r.
// Uses a 256KB buffer to handle large SSE data lines.
func newLineScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	return scanner
}

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
