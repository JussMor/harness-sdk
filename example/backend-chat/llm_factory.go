package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	ab "github.com/everfaz/autobuild-sdk"
	sdkllm "github.com/everfaz/autobuild-sdk/providers/llm"
)

// RuntimeLogContext carries identifiers for structured logging.
type RuntimeLogContext struct {
	ChatID int64
	RunID  string
	Mode   string
}

// EchoLLM is a no-op LLM for testing without an API key.
type EchoLLM struct {
	Model string
}

func (e *EchoLLM) Chat(_ context.Context, req ab.ChatRequest) (*ab.ChatResponse, error) {
	var prompt string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == ab.RoleUser {
			prompt = req.Messages[i].Content
			break
		}
	}
	return &ab.ChatResponse{
		Content:      fmt.Sprintf("[%s] Entendido: %q", e.Model, strings.TrimSpace(prompt)),
		FinishReason: "stop",
		Model:        e.Model,
		Usage:        ab.TokenUsage{PromptTokens: 32, CompletionTokens: 48, TotalTokens: 80},
	}, nil
}

// BuildLLMFromEnv builds the LLM provider from environment variables.
// Uses SDK-native providers for streaming support.
//
//   ANTHROPIC_API_KEY → providers/llm/anthropic (real ChatStream)
//   OPENAI_API_KEY    → providers/llm/openai (real SSE streaming)
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

	// OpenAI-compatible via SDK provider (real SSE streaming, vision, multi-turn tool calls)
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		routedProviders["openai"] = sdkllm.NewOpenAI(
			getenv("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			key,
			getenv("BACKEND_MODEL", "gpt-4o"),
		)
	}

	// Ollama via SDK provider — Chat + ChatStream (NDJSON), native tools, vision
	ollamaProvider := sdkllm.NewOllama(getenv("OLLAMA_MODEL", "llama3.1"))
	ollamaProvider.BaseURL = getenv("OLLAMA_BASE_URL", "http://localhost:11434")
	// Most modern Ollama models support native tool-calling. Keep this
	// configurable to allow quick fallback for models that don't.
	ollamaProvider.NativeToolCalls = strings.ToLower(getenv("OLLAMA_NATIVE_TOOL_CALLS", "true")) != "false"
	routedProviders["ollama"] = ollamaProvider

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
