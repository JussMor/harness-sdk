package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	ab "github.com/everfaz/autobuild-sdk"
)

type EchoLLM struct {
	Model string
}

type AssistantRunResult struct {
	Content string          `json:"content"`
	Reasoning string        `json:"reasoning,omitempty"`
	Runners []RunnerSummary `json:"runners,omitempty"`
	Trace   []TraceStep     `json:"trace,omitempty"`
}

type RuntimeLogContext struct {
	ChatID int64
	RunID  string
	Mode   string
}

type TraceStep = ab.ReasoningStep

func (e *EchoLLM) Chat(_ context.Context, req ab.ChatRequest) (*ab.ChatResponse, error) {
	var prompt string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == ab.RoleUser {
			prompt = req.Messages[i].Content
			break
		}
	}

	content := fmt.Sprintf("[%s] Entendido. Esta es una respuesta inicial para: %q", e.Model, strings.TrimSpace(prompt))
	return &ab.ChatResponse{
		Content:      content,
		FinishReason: "stop",
		Model:        e.Model,
		Usage: ab.TokenUsage{
			PromptTokens:     32,
			CompletionTokens: 48,
			TotalTokens:      80,
		},
	}, nil
}

func shouldUseFormalPlan(ctx context.Context, llm ab.LLMProvider, messages []ab.ChatMessage, model string) (bool, []string) {
	// Find latest user prompt in history
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

	// Single LLM call: decide parallelism + propose concrete subtasks
	analysisMessages := []ab.ChatMessage{
		{
			Role: ab.RoleSystem,
			Content: `You are a task decomposition assistant. Respond ONLY with valid JSON, no markdown, no explanation. 

Respond with:
{"parallel": true/false, "count": number, "tasks": [array of strings]}

Rules:
- parallel: true only if multiple independent subtasks that don't depend on each other
- count: number of subtasks (0 if parallel=false)
- tasks: array of specific, concrete subtask descriptions (empty if parallel=false)
- IMPORTANT: Output ONLY the JSON object, no markdown, no code blocks, no text

Example valid response for "Write 2 files":
{"parallel":true,"count":2,"tasks":["Write README.md with project overview","Write LICENSE file"]}

Example valid response for "ping":
{"parallel":false,"count":0,"tasks":[]}`,
		},
		{
			Role: ab.RoleUser,
			Content: userPrompt,
		},
	}

	resp, err := llm.Chat(ctx, ab.ChatRequest{
		Messages:    analysisMessages,
		Model:       model,
		Temperature: 0,
	})
	if err != nil {
		log.Printf("shouldUseFormalPlan LLM error: %v", err)
		return false, nil
	}

	// Parse JSON response
	var result struct {
		Parallel bool     `json:"parallel"`
		Count    int      `json:"count"`
		Tasks    []string `json:"tasks"`
	}
	
	content := strings.TrimSpace(resp.Content)
	
	// Extract JSON from markdown code blocks if present
	if strings.Contains(content, "```") {
		start := strings.Index(content, "{")
		end := strings.LastIndex(content, "}") + 1
		if start != -1 && end > start {
			content = content[start:end]
		}
	}
	
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		log.Printf("shouldUseFormalPlan JSON parse error: %v, content=%q", err, content)
		return false, nil
	}

	log.Printf("shouldUseFormalPlan analysis: prompt=%q parallel=%v count=%d tasks=%v", userPrompt, result.Parallel, result.Count, result.Tasks)
	
	if !result.Parallel || result.Count < 2 || len(result.Tasks) < 2 {
		return false, nil
	}
	
	return true, result.Tasks
}

func GenerateAssistantReply(ctx context.Context, llm ab.LLMProvider, messages []ab.ChatMessage, mode, model string, logContext RuntimeLogContext, onRuntimeEvent func(ab.Event), onTrace func(TraceStep)) AssistantRunResult {
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
	result, err := runWithMode(ctx, provider, requestedMode, model, messages, logContext, onRuntimeEvent, onTrace)
	if err != nil && requestedMode != "balanced" {
		logContext.Mode = "balanced"
		result, err = runWithMode(ctx, provider, "balanced", model, messages, logContext, onRuntimeEvent, onTrace)
	}
	if err != nil {
		return AssistantRunResult{Content: fmt.Sprintf("[%s] Error ejecutando SDK: %v", model, err)}
	}
	return result
}

func runWithMode(ctx context.Context, provider ab.LLMProvider, mode, model string, messages []ab.ChatMessage, logContext RuntimeLogContext, onRuntimeEvent func(ab.Event), onTrace func(TraceStep)) (AssistantRunResult, error) {
	engine, runtime, err := newModeEngine(provider, model, logContext)
	if err != nil {
		return AssistantRunResult{}, err
	}

	var subscriptions []*ab.Subscription
	if runtime.events != nil && onRuntimeEvent != nil {
		subscriptions = append(subscriptions, runtime.events.Subscribe(ab.EventRunnerCompleted, onRuntimeEvent))
		subscriptions = append(subscriptions, runtime.events.Subscribe(ab.EventRunnerFailed, onRuntimeEvent))
		subscriptions = append(subscriptions, runtime.events.Subscribe(ab.EventExecutableUpdated, onRuntimeEvent))
	}
	if runtime.events != nil && onTrace != nil {
		subscriptions = append(subscriptions, ab.SubscribeTransformed(runtime.events, ab.EventAgentTraceStep, func(event ab.Event) (any, bool) {
			step, ok := event.Payload["step"].(ab.ReasoningStep)
			if !ok {
				return nil, false
			}
			return TraceStep(step), true
		}, func(value any) {
			step, ok := value.(TraceStep)
			if !ok {
				return
			}
			onTrace(step)
		}))
	}
	defer func() {
		for _, sub := range subscriptions {
			sub.Cancel()
		}
	}()

	// Determine if formal plan is needed by asking LLM
	useFormalPlan, proposedTasks := shouldUseFormalPlan(ctx, provider, messages, model)
	log.Printf("formal_plan decision: useFormalPlan=%v tasks=%v", useFormalPlan, proposedTasks)
	
	planRunners := []RunnerSummary{}
	planSummary := ""
	
	if useFormalPlan && len(proposedTasks) > 0 {
		var err error
		planRunners, planSummary, err = executeFormalPlanWithTasks(ctx, engine, runtime, messages, model, proposedTasks)
		if err != nil {
			return AssistantRunResult{}, err
		}
		if len(planRunners) > 0 {
			// Formal workflow already executed and collected runners.
			// Return immediately to avoid a second expensive agent loop.
			content := buildFormalPlanResponse(planSummary, planRunners)
			return AssistantRunResult{Content: content, Runners: planRunners}, nil
		}
	}

	enhancedPrompt := strings.TrimSpace(`
Regla de salida obligatoria:
- No describas acciones futuras como si ya estuvieran hechas.
- Si usas herramientas, reporta solo acciones ejecutadas y su resultado.
- Si falta información para ejecutar, pide solo los datos faltantes sin decir "voy a ejecutar".
- Solo puedes usar y mencionar herramientas realmente cargadas en este backend.
- Si una herramienta aparece en el texto del modo pero no está cargada, di claramente que no está disponible.
- Este backend ejecuta subtareas en la capa formal (workflow + plan); no existe la herramienta spawn-runner expuesta al modelo.

Herramientas disponibles en este backend:
`+runtime.tools.DescribeAvailable()+`

Instrucción para el último mensaje del usuario:
`) + "\n"

	enhancedMessages := make([]ab.ChatMessage, len(messages))
	copy(enhancedMessages, messages)
	if strings.TrimSpace(planSummary) != "" {
		enhancedMessages = append(enhancedMessages, ab.ChatMessage{
			Role:    ab.RoleSystem,
			Content: planSummary,
		})
	}
	for i := len(enhancedMessages) - 1; i >= 0; i-- {
		if enhancedMessages[i].Role == ab.RoleUser {
			enhancedMessages[i].Content = enhancedPrompt + enhancedMessages[i].Content
			break
		}
	}

	result, err := ab.RunAgentLoopWithEngine(ctx, engine, mode, ab.AgentLoopConfig{
		Model:    model,
		MaxTurns: 6,
	}, enhancedMessages)
	if err != nil {
		return AssistantRunResult{}, err
	}

	runners := planRunners
	if len(runners) == 0 {
		runners = runtime.threads.Wait(5 * time.Second)
	}
	content := strings.TrimSpace(result.FinalContent)
	if content == "" && len(runners) > 0 {
		content = fmt.Sprintf("Se ejecutaron %d runners en paralelo.", len(runners))
	}

	return AssistantRunResult{Content: content, Reasoning: strings.TrimSpace(result.ProviderReasoning), Runners: runners, Trace: result.ReasoningTrace}, nil
}

func buildFormalPlanResponse(planSummary string, runners []RunnerSummary) string {
	parts := []string{}
	trimmedSummary := strings.TrimSpace(planSummary)
	if trimmedSummary != "" {
		parts = append(parts, trimmedSummary)
	}

	parts = append(parts, fmt.Sprintf("Runners ejecutados: %d", len(runners)))
	for i, runner := range runners {
		task := strings.TrimSpace(runner.Task)
		if task == "" {
			task = fmt.Sprintf("runner_%d", i+1)
		}

		status := strings.TrimSpace(runner.Status)
		if status == "" {
			status = "unknown"
		}

		result := strings.TrimSpace(runner.Result)
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
