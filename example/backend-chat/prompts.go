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
	Content   string          `json:"content"`
	Reasoning string          `json:"reasoning,omitempty"`
	Runners   []RunnerSummary `json:"runners,omitempty"`
	Trace     []TraceStep     `json:"trace,omitempty"`
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
	onRuntimeEvent func(ab.Event),
	onTrace func(TraceStep),
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

	result, err := runWithMode(ctx, provider, requestedMode, model, messages, logContext, onRuntimeEvent, onTrace, db)
	if err != nil && requestedMode != "balanced" {
		logContext.Mode = "balanced"
		result, err = runWithMode(ctx, provider, "balanced", model, messages, logContext, onRuntimeEvent, onTrace, db)
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
	onRuntimeEvent func(ab.Event),
	onTrace func(TraceStep),
	db *sql.DB,
) (AssistantRunResult, error) {
	_, agentRT, err := newModeEngineWithDB(provider, model, logContext, db)
	if err != nil {
		return AssistantRunResult{}, err
	}

	// Subscribe to events for frontend push
	var subs []*ab.Subscription
	if agentRT.events != nil {
		if onRuntimeEvent != nil {
			subs = append(subs,
				agentRT.events.Subscribe(ab.EventSubagentCompleted, onRuntimeEvent),
				agentRT.events.Subscribe(ab.EventExecutableUpdated, onRuntimeEvent),
			)
		}
		if onTrace != nil {
			subs = append(subs,
				ab.SubscribeTransformed(agentRT.events, ab.EventAgentTraceStep,
					func(e ab.Event) (any, bool) {
						step, ok := e.Payload["step"].(ab.ReasoningStep)
						return TraceStep(step), ok
					},
					func(v any) {
						if step, ok := v.(TraceStep); ok {
							onTrace(step)
						}
					},
				),
			)
		}
	}
	defer func() {
		for _, s := range subs {
			s.Cancel()
		}
	}()

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

	var planRunners []RunnerSummary
	var planSummary string
	if rr.PlanProposed != nil {
		planRunners, planSummary, err = executeFormalPlanFromProposedPlan(ctx, agentRT.execCtx, agentRT, rr.PlanProposed, model)
		if err != nil {
			return AssistantRunResult{}, err
		}
	}

	content := strings.TrimSpace(rr.Response)
	if len(planRunners) > 0 {
		formalContent := buildFormalPlanResponse(planSummary, planRunners)
		if content == "" {
			content = formalContent
		} else {
			content = strings.TrimSpace(content + "\n\n" + formalContent)
		}
	}

	return AssistantRunResult{Content: content, Runners: planRunners}, nil
}

func buildFormalPlanResponse(planSummary string, runners []RunnerSummary) string {
	var parts []string
	if t := strings.TrimSpace(planSummary); t != "" {
		parts = append(parts, t)
	}
	parts = append(parts, fmt.Sprintf("Runners ejecutados: %d", len(runners)))
	for i, r := range runners {
		task := strings.TrimSpace(r.Task)
		if task == "" {
			task = fmt.Sprintf("runner_%d", i+1)
		}
		status := r.Status
		if status == "" {
			status = "unknown"
		}
		result := r.Result
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
