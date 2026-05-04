package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	ab "github.com/everfaz/autobuild-sdk"
	sdkllm "github.com/everfaz/autobuild-sdk/providers/llm"
	agent "github.com/everfaz/backend-chat/ai-sdk"
)

type agentProviderAdapter struct {
	provider agent.Provider
}

func (a *agentProviderAdapter) Chat(ctx context.Context, req ab.ChatRequest) (*ab.ChatResponse, error) {
	agReq := toAgentRequest(req, req.Model)
	agResp, err := a.provider.Chat(ctx, agReq)
	if err != nil {
		return nil, err
	}

	resp := &ab.ChatResponse{
		Content:      agResp.Message.Content,
		Reasoning:    agResp.Reasoning,
		FinishReason: "stop",
		Model:        agResp.Model,
		Usage: ab.TokenUsage{
			PromptTokens:     agResp.PromptEvalCount,
			CompletionTokens: agResp.EvalCount,
			TotalTokens:      agResp.PromptEvalCount + agResp.EvalCount,
		},
	}

	for _, tc := range agResp.Message.ToolCalls {
		argsJSON, _ := json.Marshal(tc.Function.Arguments)
		resp.ToolCalls = append(resp.ToolCalls, ab.ToolCallEntry{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: string(argsJSON),
		})
	}
	if len(resp.ToolCalls) > 0 {
		resp.FinishReason = "tool_calls"
	}

	return resp, nil
}

// BuildLLMFromEnv builds the LLM provider from environment variables.
// When ANTHROPIC_API_KEY is set, uses the SDK's native Anthropic provider
// (which supports real token streaming via ChatStream). Falls back to the
// ai-sdk adapter for other providers.
func BuildLLMFromEnv() ab.LLMProvider {
	defaultProvider := strings.ToLower(getenv("BACKEND_LLM_PROVIDER", "anthropic"))
	routedProviders := map[string]ab.LLMProvider{}

	// Anthropic: use SDK provider directly (supports real streaming)
	if key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); key != "" {
		model := getenv("BACKEND_MODEL", "claude-sonnet-4-20250514")
		// Strip provider prefix if present
		if idx := strings.Index(model, "/"); idx >= 0 {
			model = model[idx+1:]
		}
		routedProviders["anthropic"] = sdkllm.NewAnthropic(key, model)
	}

	// OpenAI: use ai-sdk adapter (no streaming support yet)
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		prov := agent.NewOpenAICompatProvider(agent.ProviderConfig{
			Name:    "openai",
			BaseURL: getenv("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			APIKey:  key,
		})
		routedProviders["openai"] = &agentProviderAdapter{provider: prov}
	}

	// Ollama: use SDK provider (supports Ollama native API)
	ollamaURL := getenv("OLLAMA_BASE_URL", "http://localhost:11434")
	ollamaModel := getenv("OLLAMA_MODEL", "llama3.1")
	routedProviders["ollama"] = sdkllm.NewOllama(ollamaModel)
	_ = ollamaURL // Ollama provider uses default localhost

	// Ensure defaultProvider exists
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

func toAgentRequest(req ab.ChatRequest, modelName string) agent.ChatRequest {
	msgs := make([]agent.ChatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		agm := agent.ChatMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			agm.ToolCalls = append(agm.ToolCalls, agent.ToolCall{
				ID: tc.ID,
				Function: agent.ToolCallFunction{
					Name: tc.Name,
					Arguments: func() map[string]any {
						var args map[string]any
						_ = json.Unmarshal([]byte(tc.Arguments), &args)
						return args
					}(),
				},
			})
		}
		msgs = append(msgs, agm)
	}

	tools := make([]agent.ToolDef, 0, len(req.Tools))
	for _, t := range req.Tools {
		props := map[string]agent.ToolParam{}
		for name, p := range t.Function.Parameters.Properties {
			props[name] = agent.ToolParam{Type: p.Type, Description: p.Description}
		}
		tools = append(tools, agent.ToolDef{
			Type: t.Type,
			Function: agent.ToolFunction{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters: agent.ToolFuncParams{
					Type:       t.Function.Parameters.Type,
					Properties: props,
					Required:   t.Function.Parameters.Required,
				},
			},
		})
	}

	return agent.ChatRequest{
		Model:    modelName,
		Messages: msgs,
		Tools:    tools,
		Stream:   false,
		Options: &agent.ModelOptions{
			Temperature: req.Temperature,
			NumCtx:      req.MaxTokens,
		},
	}
}
