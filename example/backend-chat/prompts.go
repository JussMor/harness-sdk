package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	ab "github.com/everfaz/autobuild-sdk"
)

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

// AssistantRunResult is what handleRun returns to the frontend.
type AssistantRunResult struct {
	Content   string              `json:"content"`
	Reasoning string              `json:"reasoning,omitempty"`
	Runners   []ab.SubagentResult `json:"runners,omitempty"`
	Trace     []TraceStep         `json:"trace,omitempty"`
}

// RuntimeLogContext carries identifiers for structured logging.
type RuntimeLogContext struct {
	ChatID int64
	RunID  string
	Mode   string
}

type TraceStep = ab.ReasoningStep

// GenerateAssistantReply drives the full SDK Runtime lifecycle for one turn.
func GenerateAssistantReply(
	ctx context.Context,
	llm ab.LLMProvider,
	messages []ab.ChatMessage,
	mode, model string,
	logContext RuntimeLogContext,
	db *sql.DB,
) AssistantRunResult {
	if len(messages) == 0 {
		return AssistantRunResult{Content: "Necesito un prompt para responder."}
	}

	provider := llm
	if provider == nil {
		provider = &EchoLLM{Model: model}
	}
	requestedMode := strings.TrimSpace(mode)
	if requestedMode == "" {
		requestedMode = "balanced"
	}
	logContext.Mode = requestedMode

	result, err := runWithMode(ctx, provider, requestedMode, model, messages, logContext, db)
	if err != nil && requestedMode != "balanced" {
		logContext.Mode = "balanced"
		result, err = runWithMode(ctx, provider, "balanced", model, messages, logContext, db)
	}
	if err != nil {
		return AssistantRunResult{Content: fmt.Sprintf("[%s] Error: %v", model, err)}
	}
	return result
}

func runWithMode(
	ctx context.Context,
	provider ab.LLMProvider,
	mode, model string,
	messages []ab.ChatMessage,
	logContext RuntimeLogContext,
	db *sql.DB,
) (AssistantRunResult, error) {
	_, agentRT, err := newModeEngineWithDB(provider, model, logContext, db)
	if err != nil {
		return AssistantRunResult{}, err
	}

	// Main path via Runtime.Run
	userMessage := latestUserPrompt(messages)
	if userMessage == "" {
		return AssistantRunResult{Content: "No user message found."}, nil
	}

	// Load persisted conversation or create from history
	// messagesFromAB converts []ab.ChatMessage → []Message for LoadOrCreateConversation
	conv, err := LoadOrCreateConversation(ctx, agentRT.convStore, logContext.ChatID, abToMessages(messages))
	if err != nil {
		// Fallback: fresh conversation
		conv = ab.NewConversation(ConversationID(logContext.ChatID))
	}

	rr, err := agentRT.runtime.Run(ctx, conv, userMessage)
	if err != nil {
		return AssistantRunResult{}, err
	}

	var planRunners []ab.SubagentResult
	if rr.PlanProposed != nil {
		planRunners, err = executeFormalPlanFromProposedPlan(ctx, agentRT, rr.PlanProposed)
		if err != nil {
			return AssistantRunResult{}, err
		}
	}

	content := strings.TrimSpace(rr.Response)
	if len(planRunners) > 0 {
		formalContent := buildFormalPlanResponse(planRunners)
		if content == "" {
			content = formalContent
		} else {
			content = strings.TrimSpace(content + "\n\n" + formalContent)
		}
	}

	return AssistantRunResult{Content: content, Runners: planRunners}, nil
}

func buildFormalPlanResponse(runners []ab.SubagentResult) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("Runners ejecutados: %d", len(runners)))
	for i, r := range runners {
		task := strings.TrimSpace(r.Task)
		if task == "" {
			task = fmt.Sprintf("runner_%d", i+1)
		}
		status := "success"
		result := r.Output
		if r.Error != nil {
			status = "failure"
			result = r.Error.Error()
		}
		if strings.TrimSpace(result) == "" {
			status = "unknown"
		}
		if result == "" {
			result = "Sin resultado textual."
		}
		if len(result) > 220 {
			result = result[:220] + "..."
		}
		parts = append(parts, fmt.Sprintf("%d. [%s] %s: %s", i+1, status, task, result))
	}
	return strings.Join(parts, "\n")
}
