package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const anthropicBaseURL = "https://api.anthropic.com/v1"
const anthropicVersion = "2023-06-01"

// AnthropicProvider implements Provider for the Anthropic Messages API.
// Anthropic uses a completely different wire format from OpenAI:
//   - Endpoint: POST /v1/messages (not /v1/chat/completions)
//   - Auth: x-api-key header (not Authorization: Bearer)
//   - System prompt is a top-level field, not a message
//   - Tool schema uses input_schema instead of parameters
//   - Response content is an array of typed blocks
type AnthropicProvider struct {
	apiKey     string
	httpClient *http.Client
	// sem limits concurrent in-flight requests to Anthropic.
	// Anthropic rate limits are org-wide; without this, parallel goroutines
	// each backoff independently while others continue hammering the API.
	sem chan struct{}
}

// anthropicConcurrencyLimit is the max number of simultaneous Anthropic API
// calls. Keep this low — Anthropic rate limits by tokens-per-minute org-wide,
// and parallel requests compound usage faster than serial backoff can recover.
const anthropicConcurrencyLimit = 3

// NewAnthropicProvider creates a provider from config.
func NewAnthropicProvider(cfg ProviderConfig) Provider {
	return &AnthropicProvider{
		apiKey: cfg.APIKey,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
		sem: make(chan struct{}, anthropicConcurrencyLimit),
	}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

// ── Anthropic wire types (request) ──────────────────────────────────────────

type anthropicRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	// System is sent as an array of text blocks so we can attach
	// cache_control to the last block (caches the whole system prefix).
	System   []anthropicSystemBlock `json:"system,omitempty"`
	Messages []anthropicMessage     `json:"messages"`
	Tools    []anthropicTool        `json:"tools,omitempty"`
}

// anthropicCacheControl marks a block for prompt caching. Type is always
// "ephemeral" today (5-minute TTL). When attached to the last item in a
// stable prefix (system, tools), Anthropic caches everything up to and
// including that block. Cache hits cost 10% of normal input tokens and
// count 10% against the org TPM — the main lever for our rate-limit issue.
type anthropicCacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

type anthropicSystemBlock struct {
	Type         string                 `json:"type"` // "text"
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content anthropicContent `json:"content"`
}

// anthropicContent can be a plain string or a slice of content blocks.
// We use the block form when we need tool_use/tool_result blocks.
type anthropicContent = any // string | []anthropicBlock

type anthropicBlock struct {
	Type      string `json:"type"` // "text" | "thinking" | "tool_use" | "tool_result"
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	ID        string `json:"id,omitempty"`          // tool_use block
	Name      string `json:"name,omitempty"`        // tool_use block
	Input     any    `json:"input,omitempty"`       // tool_use block
	ToolUseID string `json:"tool_use_id,omitempty"` // tool_result block
	Content   string `json:"content,omitempty"`     // tool_result block
}

type anthropicTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	InputSchema  ToolFuncParams         `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

// ── Anthropic wire types (response) ─────────────────────────────────────────

type anthropicResponse struct {
	ID         string           `json:"id"`
	Type       string           `json:"type"`
	Role       string           `json:"role"`
	Content    []anthropicBlock `json:"content"`
	Model      string           `json:"model"`
	StopReason string           `json:"stop_reason"` // "end_turn" | "tool_use" | "max_tokens"
	Usage      anthropicUsage   `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// ── Chat ─────────────────────────────────────────────────────────────────────

func (p *AnthropicProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	// Acquire the global concurrency semaphore before touching the API.
	// This ensures at most anthropicConcurrencyLimit goroutines are in-flight
	// simultaneously, preventing org-wide rate limits from compounding.
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return ChatResponse{}, fmt.Errorf("anthropic: context cancelled while waiting for slot: %w", ctx.Err())
	}
	defer func() { <-p.sem }()

	// Extract system prompt from messages (Anthropic requires it as a top-level field).
	system, msgs := splitSystemMessages(req.Messages)

	maxTokens := 8192
	if req.Options != nil && req.Options.NumCtx > 0 {
		maxTokens = req.Options.NumCtx
	}

	// Build system blocks with cache_control on the last block so the
	// entire system prefix is cached across iterations of the same thread.
	var systemBlocks []anthropicSystemBlock
	if system != "" {
		systemBlocks = []anthropicSystemBlock{{
			Type:         "text",
			Text:         system,
			CacheControl: &anthropicCacheControl{Type: "ephemeral"},
		}}
	}

	aReq := anthropicRequest{
		Model:     req.Model,
		MaxTokens: maxTokens,
		System:    systemBlocks,
		Messages:  toAnthropicMessages(msgs),
		Tools:     toAnthropicTools(req.Tools),
	}

	body, err := json.Marshal(aReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	slog.Debug("anthropic request", "model", req.Model, "messages", len(msgs), "tools", len(req.Tools))

	const maxRetries = 4
	var resp *http.Response
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Rebuild request body for each attempt (body reader is consumed after first use).
		attempReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicBaseURL+"/messages", bytes.NewReader(body))
		if err != nil {
			return ChatResponse{}, fmt.Errorf("anthropic: build request: %w", err)
		}
		attempReq.Header.Set("Content-Type", "application/json")
		attempReq.Header.Set("x-api-key", p.apiKey)
		attempReq.Header.Set("anthropic-version", anthropicVersion)

		resp, err = p.httpClient.Do(attempReq)
		if err != nil {
			return ChatResponse{}, fmt.Errorf("anthropic: http call: %w", err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			errBody, _ := io.ReadAll(resp.Body)
			// Prefer the server-provided retry-after; fall back to exponential.
			// Anthropic also exposes anthropic-ratelimit-*-reset timestamps, but
			// retry-after is the canonical "wait this long" signal.
			backoff := parseRetryAfter(resp.Header.Get("retry-after"))
			if backoff <= 0 {
				backoff = time.Duration(1<<uint(attempt)) * 15 * time.Second // 15s, 30s, 60s, 120s
			}
			// Cap to avoid unbounded waits if the server returns something silly.
			if backoff > 5*time.Minute {
				backoff = 5 * time.Minute
			}
			resp.Body.Close()
			slog.Warn("anthropic: rate limited, backing off",
				"attempt", attempt+1,
				"backoff", backoff,
				"retry_after_hdr", resp.Header.Get("retry-after"),
				"input_tokens_limit", resp.Header.Get("anthropic-ratelimit-input-tokens-limit"),
				"input_tokens_remaining", resp.Header.Get("anthropic-ratelimit-input-tokens-remaining"),
				"input_tokens_reset", resp.Header.Get("anthropic-ratelimit-input-tokens-reset"),
				"detail", truncateAnthropicDetail(string(errBody), 200),
			)
			select {
			case <-ctx.Done():
				return ChatResponse{}, fmt.Errorf("anthropic: rate limited (context cancelled during backoff): %w", ctx.Err())
			case <-time.After(backoff):
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return ChatResponse{}, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, string(errBody))
		}

		break // success
	}
	if resp == nil || resp.StatusCode == http.StatusTooManyRequests {
		return ChatResponse{}, fmt.Errorf("anthropic: rate limit exceeded after %d retries — reduce prompt length or wait before retrying", maxRetries)
	}
	defer resp.Body.Close()

	var aResp anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&aResp); err != nil {
		return ChatResponse{}, fmt.Errorf("anthropic: decode response: %w", err)
	}

	chatResp := ChatResponse{
		Model:           req.Model,
		Message:         fromAnthropicResponse(aResp),
		Done:            true,
		Reasoning:       extractAnthropicReasoning(aResp),
		EvalCount:       aResp.Usage.OutputTokens,
		PromptEvalCount: aResp.Usage.InputTokens,
	}

	// Surface cache effectiveness at INFO so it shows up alongside iteration
	// telemetry. cache_read = tokens served from cache (charged at 10% TPM).
	// cache_creation = tokens written to cache on first call (1.25x cost, but
	// then cheap on every subsequent call within the 5-minute TTL).
	slog.Info("anthropic response",
		"model", req.Model,
		"stop_reason", aResp.StopReason,
		"input_tokens", aResp.Usage.InputTokens,
		"output_tokens", aResp.Usage.OutputTokens,
		"cache_read", aResp.Usage.CacheReadInputTokens,
		"cache_creation", aResp.Usage.CacheCreationInputTokens,
		"tool_calls", len(chatResp.Message.ToolCalls),
	)

	return chatResp, nil
}

// ── Conversion helpers ───────────────────────────────────────────────────────

// truncateAnthropicDetail shortens s to at most n bytes for log-safe display.
// (package-level truncate lives in tools.go — this avoids a redeclaration)
func truncateAnthropicDetail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// parseRetryAfter parses an HTTP Retry-After header value, which may be either
// a number of seconds or an HTTP-date. Returns 0 if the value is missing or
// unparseable so the caller can fall back to its own backoff schedule.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// isRateLimit returns true when the error message looks like a 429 from
// Anthropic (used by the caller to decide whether to surface a hint).
func isRateLimit(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "rate limit") || strings.Contains(s, "429")
}

// splitSystemMessages extracts all system messages into a single string and
// returns the remaining non-system messages.
func splitSystemMessages(msgs []ChatMessage) (system string, rest []ChatMessage) {
	for _, m := range msgs {
		if m.Role == "system" {
			if system != "" {
				system += "\n"
			}
			system += m.Content
		} else {
			rest = append(rest, m)
		}
	}
	return system, rest
}

func toAnthropicMessages(msgs []ChatMessage) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		am := toAnthropicMessage(m)
		if am == nil {
			continue
		}
		out = append(out, *am)
	}
	return out
}

func toAnthropicMessage(m ChatMessage) *anthropicMessage {
	switch m.Role {
	case "system":
		// Already extracted; skip.
		return nil

	case "tool":
		// Tool result: Anthropic expects role=user with a tool_result block.
		toolUseID := m.ToolCallID
		content := m.Content
		if toolUseID == "" {
			// Legacy fallback: content prefix "toolu_N:" convention.
			toolUseID = extractToolUseID(m.Content)
			if toolUseID != "" {
				content = stripToolUseIDPrefix(m.Content)
			}
		}
		return &anthropicMessage{
			Role: "user",
			Content: []anthropicBlock{
				{
					Type:      "tool_result",
					ToolUseID: toolUseID,
					Content:   content,
				},
			},
		}

	case "assistant":
		if len(m.ToolCalls) == 0 {
			return &anthropicMessage{Role: "assistant", Content: m.Content}
		}
		// Assistant with tool calls: mix text + tool_use blocks.
		blocks := []anthropicBlock{}
		if m.Content != "" {
			blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
		}
		for i, tc := range m.ToolCalls {
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("toolu_%d", i)
			}
			blocks = append(blocks, anthropicBlock{
				Type:  "tool_use",
				ID:    id,
				Name:  tc.Function.Name,
				Input: tc.Function.Arguments,
			})
		}
		return &anthropicMessage{Role: "assistant", Content: blocks}
	default: // "user"
		return &anthropicMessage{Role: m.Role, Content: m.Content}
	}
}

func toAnthropicTools(defs []ToolDef) []anthropicTool {
	if len(defs) == 0 {
		return nil
	}
	out := make([]anthropicTool, 0, len(defs))
	for _, d := range defs {
		out = append(out, anthropicTool{
			Name:        d.Function.Name,
			Description: d.Function.Description,
			InputSchema: d.Function.Parameters,
		})
	}
	// Cache the entire tool block by marking the last tool. Anthropic
	// caches all preceding tools when the last one carries cache_control.
	out[len(out)-1].CacheControl = &anthropicCacheControl{Type: "ephemeral"}
	return out
}

func fromAnthropicResponse(aResp anthropicResponse) ChatMessage {
	cm := ChatMessage{Role: "assistant"}
	for _, block := range aResp.Content {
		switch block.Type {
		case "text":
			cm.Content += block.Text
		case "tool_use":
			args, _ := toMap(block.Input)
			cm.ToolCalls = append(cm.ToolCalls, ToolCall{
				ID: block.ID,
				Function: ToolCallFunction{
					Name:      block.Name,
					Arguments: args,
				},
			})
		}
	}
	return cm
}

func extractAnthropicReasoning(aResp anthropicResponse) string {
	parts := make([]string, 0, len(aResp.Content))
	for _, block := range aResp.Content {
		if block.Type == "thinking" && strings.TrimSpace(block.Thinking) != "" {
			parts = append(parts, strings.TrimSpace(block.Thinking))
		}
	}
	return strings.Join(parts, "\n\n")
}

// toMap converts any JSON-compatible value to map[string]any.
func toMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	// Re-marshal/unmarshal to normalize.
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false
	}
	return m, true
}

// extractToolUseID looks for a "toolu_N:" prefix in a tool result content string.
// This lets us round-trip the Anthropic tool_use IDs through the ChatMessage.Content field.
func extractToolUseID(content string) string {
	for i, c := range content {
		if c == ':' && i > 0 {
			prefix := content[:i]
			if len(prefix) > 4 && prefix[:5] == "toolu" {
				return prefix
			}
			return ""
		}
	}
	return ""
}

func stripToolUseIDPrefix(content string) string {
	for i, c := range content {
		if c == ':' && i > 0 {
			prefix := content[:i]
			if len(prefix) > 4 && prefix[:5] == "toolu" {
				return content[i+1:]
			}
			return content
		}
	}
	return content
}
