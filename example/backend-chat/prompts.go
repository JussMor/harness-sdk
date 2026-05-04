package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
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

	// Parallel subagents path
	useFormalPlan, proposedTasks := shouldUseFormalPlan(ctx, provider, messages, model)
	log.Printf("formal_plan: useFormalPlan=%v tasks=%v", useFormalPlan, proposedTasks)

	var planRunners []RunnerSummary
	var planSummary string

	if useFormalPlan && len(proposedTasks) > 0 {
		planRunners, planSummary, err = executeFormalPlanWithTasks(
			ctx, agentRT.execCtx, agentRT, messages, model, proposedTasks,
		)
		if err != nil {
			return AssistantRunResult{}, err
		}
		if len(planRunners) > 0 {
			return AssistantRunResult{
				Content: buildFormalPlanResponse(planSummary, planRunners),
				Runners: planRunners,
			}, nil
		}
	}

	// Single-agent path via Runtime.Run
	userMessage := latestUserPrompt(messages)
	if userMessage == "" {
		return AssistantRunResult{Content: "No user message found."}, nil
	}
	if planSummary != "" {
		userMessage = planSummary + "\n\n" + userMessage
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

	content := strings.TrimSpace(rr.Response)
	if content == "" && len(planRunners) > 0 {
		content = fmt.Sprintf("Se ejecutaron %d runners en paralelo.", len(planRunners))
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

func shouldUseFormalPlan(ctx context.Context, llm ab.LLMProvider, messages []ab.ChatMessage, model string) (bool, []string) {
	var userPrompt string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == ab.RoleUser {
			userPrompt = strings.TrimSpace(messages[i].Content)
			break
		}
	}
	if userPrompt == "" {
		return false, nil
	}

	resp, err := llm.Chat(ctx, ab.ChatRequest{
		Model: model,
		Messages: []ab.ChatMessage{
			{
				Role: ab.RoleSystem,
				Content: `Task decomposition assistant. Respond ONLY with JSON:
{"parallel": bool, "count": int, "tasks": [strings]}
parallel: true only if multiple independent subtasks.
count: number of subtasks (0 if parallel=false).
tasks: concrete subtask descriptions (empty if parallel=false).`,
			},
			{Role: ab.RoleUser, Content: userPrompt},
		},
	})
	if err != nil {
		log.Printf("shouldUseFormalPlan: %v", err)
		return false, nil
	}

	content := strings.TrimSpace(resp.Content)
	if idx := strings.Index(content, "{"); idx > 0 {
		content = content[idx:]
	}
	if idx := strings.LastIndex(content, "}"); idx >= 0 {
		content = content[:idx+1]
	}

	var result struct {
		Parallel bool     `json:"parallel"`
		Count    int      `json:"count"`
		Tasks    []string `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		log.Printf("shouldUseFormalPlan parse error: %v content=%q", err, content)
		return false, nil
	}
	if !result.Parallel || result.Count < 2 || len(result.Tasks) < 2 {
		return false, nil
	}
	return true, result.Tasks
}
