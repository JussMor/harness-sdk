package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	ab "github.com/everfaz/autobuild-sdk"
	sdkllm "github.com/everfaz/autobuild-sdk/providers/llm"
)

// BuildLLMFromEnv builds the LLM provider from environment variables.
// Uses SDK-native providers wherever available for streaming support.
//
//   ANTHROPIC_API_KEY → providers/llm/anthropic (real ChatStream)
//   OPENAI_API_KEY    → inline OpenAI-compat client (no ai-sdk)
//   always            → providers/llm/ollama (local models)
func BuildLLMFromEnv() ab.LLMProvider {
	defaultProvider := strings.ToLower(getenv("BACKEND_LLM_PROVIDER", "anthropic"))
	routedProviders := map[string]ab.LLMProvider{}

	// Anthropic via SDK provider (supports real token streaming)
	if key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); key != "" {
		model := getenv("BACKEND_MODEL", "claude-sonnet-4-20250514")
		if idx := strings.Index(model, "/"); idx >= 0 {
			model = model[idx+1:]
		}
		routedProviders["anthropic"] = sdkllm.NewAnthropic(key, model)
	}

	// OpenAI-compatible via inline client (Groq, Together, Mistral, etc.)
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		routedProviders["openai"] = newOpenAIProvider(
			getenv("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			key,
		)
	}

	// Ollama via SDK provider
	ollamaModel := getenv("OLLAMA_MODEL", "llama3.1")
	routedProviders["ollama"] = sdkllm.NewOllama(ollamaModel)

	// Ensure defaultProvider is available
	if _, ok := routedProviders[defaultProvider]; !ok {
		for _, preferred := range []string{"anthropic", "openai", "ollama"} {
			if _, exists := routedProviders[preferred]; exists {
				defaultProvider = preferred
				break
			}
		}
	}

	if len(routedProviders) == 0 {
		return &EchoLLM{Model: getenv("BACKEND_MODEL", "anthropic/claude-sonnet-4-20250514")}
	}

	return ab.NewRoutedLLMProvider(defaultProvider, routedProviders)
}

// ── Inline OpenAI-compatible provider ────────────────────────────────────────
// Implements ab.LLMProvider directly without the ai-sdk intermediary.
// Compatible with OpenAI, Groq, Together, OpenRouter, Mistral, DeepSeek.

type openAIProvider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func newOpenAIProvider(baseURL, apiKey string) *openAIProvider {
	return &openAIProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 10 * time.Minute},
	}
}

func (p *openAIProvider) Chat(ctx context.Context, req ab.ChatRequest) (*ab.ChatResponse, error) {
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		msg := map[string]any{
			"role":    string(m.Role),
			"content": m.Content,
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		if len(m.ToolCalls) > 0 {
			tcs := make([]map[string]any, 0, len(m.ToolCalls))
			for i, tc := range m.ToolCalls {
				id := tc.ID
				if id == "" {
					id = fmt.Sprintf("call_%d", i)
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
		"model":    req.Model,
		"messages": msgs,
		"stream":   false,
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

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		p.baseURL+"/chat/completions", bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("openai: request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: %d: %s", resp.StatusCode, string(errBody))
	}

	var raw struct {
		Choices []struct {
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
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
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("openai: decode: %w", err)
	}
	if len(raw.Choices) == 0 {
		return nil, fmt.Errorf("openai: empty choices")
	}

	choice := raw.Choices[0]
	out := &ab.ChatResponse{
		Content:      choice.Message.Content,
		FinishReason: choice.FinishReason,
		Model:        raw.Model,
		Usage: ab.TokenUsage{
			PromptTokens:     raw.Usage.PromptTokens,
			CompletionTokens: raw.Usage.CompletionTokens,
			TotalTokens:      raw.Usage.TotalTokens,
		},
	}
	for _, tc := range choice.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ab.ToolCallEntry{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	if len(out.ToolCalls) > 0 {
		out.FinishReason = "tool_calls"
	}
	return out, nil
}

var _ ab.LLMProvider = (*openAIProvider)(nil)
