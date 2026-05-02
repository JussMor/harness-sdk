package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	ab "github.com/everfaz/autobuild-sdk"
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

func BuildLLMFromEnv() ab.LLMProvider {
	defaultProvider := strings.ToLower(getenv("BACKEND_LLM_PROVIDER", "anthropic"))
	providers := map[string]agent.Provider{}

	if key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); key != "" {
		providers["anthropic"] = agent.NewAnthropicProvider(agent.ProviderConfig{
			Name:   "anthropic",
			APIKey: key,
		})
	}

	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		providers["openai"] = agent.NewOpenAICompatProvider(agent.ProviderConfig{
			Name:    "openai",
			BaseURL: getenv("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			APIKey:  key,
		})
	}

	providers["ollama"] = agent.NewOllamaProvider(agent.ProviderConfig{
		Name:    "ollama",
		BaseURL: getenv("OLLAMA_BASE_URL", "http://localhost:11434"),
	})

	if _, ok := providers[defaultProvider]; !ok {
		for _, preferred := range []string{"anthropic", "openai", "ollama"} {
			if _, exists := providers[preferred]; exists {
				defaultProvider = preferred
				break
			}
		}
	}

	if len(providers) == 0 {
		return &EchoLLM{Model: getenv("BACKEND_MODEL", "anthropic/claude-sonnet-4-20250514")}
	}

	routedProviders := make(map[string]ab.LLMProvider, len(providers))
	for name, provider := range providers {
		routedProviders[name] = &agentProviderAdapter{provider: provider}
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
