package main

import (
	"context"
	"fmt"
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

	planRunners, planSummary, err := executeFormalPlan(ctx, engine, runtime, messages, model)
	if err != nil {
		return AssistantRunResult{}, err
	}

	enhancedPrompt := strings.TrimSpace(`
Regla de salida obligatoria:
- No describas acciones futuras como si ya estuvieran hechas.
- Si usas herramientas, reporta solo acciones ejecutadas y su resultado.
- Si falta información para ejecutar, pide solo los datos faltantes sin decir "voy a ejecutar".
- Solo puedes usar y mencionar herramientas realmente cargadas en este backend.
- Si una herramienta aparece en el texto del modo pero no está cargada, di claramente que no está disponible.
- Este backend ejecuta subtareas en la capa formal (workflow + plan); no existe la herramienta spawn-runner expuesta al modelo.

Usa todo el historial de conversación para mantener contexto.

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
