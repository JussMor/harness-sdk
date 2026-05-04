package autobuild

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════
// AgentLoop — agnostic, minimal, extensible
//
// Zero coupling to Engine. You inject exactly what you need:
//   - LLMProvider    (required) — any backend
//   - ToolRegistry   (optional) — if the LLM can call tools
//   - SandboxDriver  (optional) — if tools need a sandbox
//   - EventBus       (optional) — if you want turn events
//
// Everything else is hooks: OnTurn, OnToolCall, OnToolResult,
// ShouldStop, BuildRequest, MaxRetries.
// ═══════════════════════════════════════════════════════════════════════

// AgentLoopConfig configures an agent loop run. Only Provider is required.
type AgentLoopConfig struct {
	// ── Required ──────────────────────────────────────────────────

	// Provider is the LLM backend to call. This is the only required field.
	Provider LLMProvider

	// ── Context (all optional) ───────────────────────────────────

	// SystemPrompt is prepended as a system message.
	SystemPrompt string

	// Model is the model identifier (e.g. "claude-sonnet-4-20250514").
	// Passed directly to ChatRequest.Model. If empty, the provider
	// uses its own default.
	Model string

	// Tools gives the LLM access to tool calling. Nil means no tools.
	Tools *ToolRegistry

	// Sandbox is passed to tool Execute functions. Nil is fine if
	// your tools don't need a sandbox.
	Sandbox SandboxDriver

	// SandboxID is the specific sandbox environment for tool execution.
	SandboxID string

	// Events is an optional EventBus to emit "agent.turn" events.
	Events EventBus

	// ── Limits ────────────────────────────────────────────────────

	// MaxTurns caps the LLM ↔ tool loop. 0 defaults to 50.
	MaxTurns int

	// MaxRetries caps LLM call retries per turn. 0 defaults to 3.
	MaxRetries int

	// ── Hooks (all optional, all nil-safe) ────────────────────────

	// OnTurn is called after each LLM response, before tool dispatch.
	// Return false to abort the loop early.
	OnTurn func(turn int, resp *ChatResponse) bool

	// OnToolCall is called before each tool execution.
	// Return false to skip this specific tool call.
	OnToolCall func(call ToolCallEntry) bool

	// OnToolResult is called after each tool execution, before the
	// result is added to the conversation. You can transform the
	// result (e.g. truncate, redact, enrich). Return the final result.
	OnToolResult func(call ToolCallEntry, result ToolResult) ToolResult

	// ShouldStop is called after each LLM response. Return true to
	// end the loop even if the LLM returned tool calls. Useful for
	// custom stop conditions (e.g. budget, time, specific content).
	// Nil means: stop only when the LLM returns no tool calls.
	ShouldStop func(turn int, resp *ChatResponse) bool

	// OnError is called when the LLM returns an error. Return true to
	// retry, false to abort. Nil means abort on error.
	OnError func(err error, attempt int) bool

	// BuildRequest customizes how the ChatRequest is built each turn.
	// If nil, a default builder is used (system + messages + tool defs).
	// Use this to inject extra context, modify tool lists per turn, etc.
	BuildRequest func(systemPrompt string, messages []ChatMessage, tools *ToolRegistry) ChatRequest
}

// AgentLoopResult is returned when the loop finishes.
type AgentLoopResult struct {
	// FinalContent is the LLM's last text response.
	FinalContent string

	// ProviderReasoning is raw reasoning content explicitly returned by the
	// provider, if any. It is not synthesized by the SDK.
	ProviderReasoning string

	// TotalTurns is how many LLM calls were made.
	TotalTurns int

	// TotalUsage is the aggregated token usage across all turns.
	TotalUsage TokenUsage

	// Messages is the full conversation history.
	Messages []ChatMessage

	// ReasoningTrace is a safe structured execution trace built from loop turns,
	// tool lifecycle, and optional provider-exposed reasoning.
	ReasoningTrace []ReasoningStep

	// StopReason explains why the loop ended: "complete", "max_turns",
	// "aborted", "stopped", "error".
	StopReason string
}

// RunAgentLoop executes the full LLM ↔ tool cycle.
//
// The loop:
//  1. Builds ChatRequest with system prompt + tool defs
//  2. Calls LLM (with retry)
//  3. If ShouldStop returns true → return with StopReason="stopped"
//  4. If LLM returns tool_calls → dispatch all → append results → goto 2
//  5. If LLM returns text only → return with StopReason="complete"
//  6. If max turns reached → return with StopReason="max_turns"
//
// The LLM decides everything: which tools, what order, when to stop.
// Your tools define what it CAN do. Your system prompt defines what it SHOULD do.
func RunAgentLoop(ctx context.Context, cfg AgentLoopConfig, messages []ChatMessage) (*AgentLoopResult, error) {
	if cfg.Provider == nil {
		return nil, fmt.Errorf("AgentLoopConfig.Provider is required")
	}

	// ─── Defaults ─────────────────────────────────────────────────
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 50
	}
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	// ─── Build initial request ────────────────────────────────────
	req := buildRequest(cfg, messages)

	// ─── Create dispatcher (only if tools are provided) ───────────
	var dispatcher *ToolDispatcher
	if cfg.Tools != nil {
		dispatcher = NewToolDispatcher(cfg.Tools, cfg.Sandbox)
	}

	// ─── The loop ─────────────────────────────────────────────────
	result := &AgentLoopResult{
		Messages: req.Messages,
		ReasoningTrace: make([]ReasoningStep, 0, 8),
	}
	traceSeq := 0
	if cfg.Events != nil {
		cfg.Events.Publish(Event{
			Type:      EventAgentLoopStarted,
			Source:    "agent_loop",
			Timestamp: time.Now(),
			Payload: map[string]any{
				"model":            cfg.Model,
				"max_turns":        maxTurns,
				"initial_messages": len(req.Messages),
			},
		})
	}
	appendTrace := func(stepType, title, content string, details []string) {
		traceSeq++
		step := ReasoningStep{
			ID:      fmt.Sprintf("trace-%d", traceSeq),
			Type:    stepType,
			Title:   title,
			Content: content,
			Details: details,
		}
		result.ReasoningTrace = append(result.ReasoningTrace, step)
		if cfg.Events != nil {
			cfg.Events.Publish(Event{
				Type:      EventAgentTraceStep,
				Source:    "agent_loop",
				Timestamp: time.Now(),
				Payload: map[string]any{
					"step": step,
				},
			})
		}
	}

	for turn := 1; turn <= maxTurns; turn++ {
		// Call LLM
		resp, err := callWithRetry(ctx, cfg.Provider, req, maxRetries, cfg.OnError)
		if err != nil {
			if cfg.Events != nil {
				cfg.Events.Publish(Event{
					Type:      EventAgentLoopFailed,
					Source:    "agent_loop",
					Timestamp: time.Now(),
					Payload: map[string]any{
						"turn":  turn,
						"error": err.Error(),
					},
				})
			}
			result.StopReason = "error"
			return result, fmt.Errorf("turn %d: %w", turn, err)
		}

		result.TotalTurns = turn
		result.TotalUsage.PromptTokens += resp.Usage.PromptTokens
		result.TotalUsage.CompletionTokens += resp.Usage.CompletionTokens
		result.TotalUsage.TotalTokens += resp.Usage.TotalTokens
		if strings.TrimSpace(resp.Reasoning) != "" {
			if result.ProviderReasoning == "" {
				result.ProviderReasoning = resp.Reasoning
			} else {
				result.ProviderReasoning += "\n\n" + resp.Reasoning
			}
			appendTrace("thinking", fmt.Sprintf("Provider reasoning %d", turn), previewReasoningText(resp.Reasoning, 220), nil)
		}
		appendTrace("thinking", fmt.Sprintf("Turn %d", turn), "", []string{
			fmt.Sprintf("Finish reason: %s", resp.FinishReason),
			fmt.Sprintf("Tool calls requested: %d", len(resp.ToolCalls)),
			fmt.Sprintf("Tokens this turn: %d", resp.Usage.TotalTokens),
		})

		// Emit event (if EventBus provided)
		if cfg.Events != nil {
			cfg.Events.Publish(Event{
				Type:      EventAgentTurnCompleted,
				Source:    "agent_loop",
				Timestamp: time.Now(),
				Payload: map[string]any{
					"turn":          turn,
					"finish_reason": resp.FinishReason,
					"tool_calls":    len(resp.ToolCalls),
					"tokens":        resp.Usage.TotalTokens,
					"model":         resp.Model,
				},
			})
			cfg.Events.Publish(Event{
				Type:      EventType("agent.turn"),
				Source:    "agent_loop",
				Timestamp: time.Now(),
				Payload: map[string]any{
					"turn":          turn,
					"finish_reason": resp.FinishReason,
					"tool_calls":    len(resp.ToolCalls),
					"tokens":        resp.Usage.TotalTokens,
				},
			})
		}

		// OnTurn callback
		if cfg.OnTurn != nil && !cfg.OnTurn(turn, resp) {
			result.FinalContent = resp.Content
			result.StopReason = "aborted"
			publishAgentLoopCompleted(cfg.Events, result)
			return result, nil
		}

		// Custom stop condition
		if cfg.ShouldStop != nil && cfg.ShouldStop(turn, resp) {
			result.FinalContent = resp.Content
			result.StopReason = "stopped"
			result.Messages = append(result.Messages, ChatMessage{
				Role: RoleAssistant, Content: resp.Content,
			})
			publishAgentLoopCompleted(cfg.Events, result)
			return result, nil
		}

		// No tool calls → LLM is done
		if len(resp.ToolCalls) == 0 {
			result.FinalContent = resp.Content
			result.StopReason = "complete"
			result.Messages = append(result.Messages, ChatMessage{
				Role: RoleAssistant, Content: resp.Content,
			})
			publishAgentLoopCompleted(cfg.Events, result)
			return result, nil
		}

		// ─── Dispatch tool calls ──────────────────────────────────
		if dispatcher == nil {
			result.FinalContent = resp.Content
			result.StopReason = "error"
			if cfg.Events != nil {
				cfg.Events.Publish(Event{
					Type:      EventAgentLoopFailed,
					Source:    "agent_loop",
					Timestamp: time.Now(),
					Payload: map[string]any{
						"turn":  turn,
						"error": "LLM requested tool calls but no ToolRegistry is configured",
					},
				})
			}
			return result, fmt.Errorf("LLM requested tool calls but no ToolRegistry is configured")
		}

		// Filter through OnToolCall
		var callsToRun []ToolCallEntry
		for _, tc := range resp.ToolCalls {
			if cfg.OnToolCall == nil || cfg.OnToolCall(tc) {
				callsToRun = append(callsToRun, tc)
				appendTrace("action", fmt.Sprintf("Tool call: %s", tc.Name), previewReasoningJSON(tc.Arguments, 220), nil)
			}
		}

		// Execute tools
		toolResults := dispatcher.DispatchAll(ctx, callsToRun, cfg.SandboxID)

		// Transform results through OnToolResult
		if cfg.OnToolResult != nil {
			for i, tr := range toolResults {
				toolResults[i] = cfg.OnToolResult(callsToRun[i], tr)
			}
		}
		for _, tr := range toolResults {
			stepType := "result"
			if tr.Error != nil {
				stepType = "thinking"
			}
			appendTrace(stepType, fmt.Sprintf("Tool result: %s", tr.Name), previewReasoningText(tr.Content, 220), nil)
		}

		// Convert to messages and append
		toolMsgs := ToMessages(resp.ToolCalls, toolResults)
		result.Messages = append(result.Messages, toolMsgs...)

		// Update request for next turn
		req = buildRequest(cfg, result.Messages)
	}

	result.StopReason = "max_turns"
	publishAgentLoopCompleted(cfg.Events, result)
	return result, nil
}

func publishAgentLoopCompleted(events EventBus, result *AgentLoopResult) {
	if events == nil || result == nil {
		return
	}

	events.Publish(Event{
		Type:      EventAgentLoopCompleted,
		Source:    "agent_loop",
		Timestamp: time.Now(),
		Payload: map[string]any{
			"stop_reason":      result.StopReason,
			"total_turns":      result.TotalTurns,
			"final_content":    result.FinalContent,
				"provider_reasoning": result.ProviderReasoning,
			"trace_steps":      len(result.ReasoningTrace),
			"prompt_tokens":    result.TotalUsage.PromptTokens,
			"completion_tokens": result.TotalUsage.CompletionTokens,
			"total_tokens":     result.TotalUsage.TotalTokens,
		},
	})
}

func previewReasoningJSON(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		pretty, err := json.Marshal(decoded)
		if err == nil {
			return previewReasoningText(string(pretty), limit)
		}
	}

	return previewReasoningText(trimmed, limit)
}

func previewReasoningText(value string, limit int) string {
	trimmed := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(trimmed) <= limit || limit <= 3 {
		return trimmed
	}
	return trimmed[:limit-3] + "..."
}

// ─── Convenience: RunAgentLoopWithEngine ──────────────────────────────
// For users who already have an Engine, this resolves Provider, Tools,
// Sandbox, Events, and Mode automatically. It's a thin wrapper.

// RunAgentLoopWithEngine is a convenience that extracts Provider, Tools,
// Sandbox, and Events from an Engine and resolves the mode. Use this when
// you have a full Engine. Use RunAgentLoop directly for maximum control.
func RunAgentLoopWithEngine(ctx context.Context, engine *Engine, modeID string, cfg AgentLoopConfig, messages []ChatMessage) (*AgentLoopResult, error) {
	// Track whether the caller already supplied a fully-assembled SystemPrompt.
	// If yes, we trust them and skip our own Build — this is what Runtime does.
	callerSuppliedPrompt := cfg.SystemPrompt != ""

	// Resolve mode → populate Model, SystemPrompt
	var resolvedModel string
	if engine.HasModes() && modeID != "" {
		mode, err := engine.Modes.Get(ctx, modeID)
		if err != nil {
			return nil, fmt.Errorf("resolve mode %q: %w", modeID, err)
		}
		if cfg.Model == "" && mode.ModelSettings != nil {
			cfg.Model = mode.ModelSettings.Model
		}
		// Only fill SystemPrompt from mode if caller didn't supply one.
		// If a builder is wired, route the mode prompt into LayerMode instead.
		if !callerSuppliedPrompt {
			if engine.HasPrompt() {
				engine.Prompt.Set(LayerMode, mode.PromptContent)
				cfg.SystemPrompt = engine.Prompt.Build()
			} else {
				cfg.SystemPrompt = mode.PromptContent
			}
		}
		resolvedModel = cfg.Model
	} else if !callerSuppliedPrompt && engine.HasPrompt() {
		// No mode but a builder exists — assemble what we have.
		cfg.SystemPrompt = engine.Prompt.Build()
	}

	// Resolve provider using model for routing.
	if cfg.Provider == nil {
		p, err := resolveProvider(engine, resolvedModel)
		if err != nil {
			return nil, err
		}
		cfg.Provider = p
	}

	if cfg.Tools == nil && engine.HasTools() {
		cfg.Tools = engine.Tools
	}
	if cfg.Sandbox == nil && engine.HasSandbox() {
		cfg.Sandbox = engine.Sandbox
	}
	if cfg.Events == nil && engine.HasEvents() {
		cfg.Events = engine.Events
	}

	return RunAgentLoop(ctx, cfg, messages)
}

// ─── Internal helpers ─────────────────────────────────────────────────

// buildRequest constructs a ChatRequest using BuildRequest hook or default logic.
func buildRequest(cfg AgentLoopConfig, messages []ChatMessage) ChatRequest {
	if cfg.BuildRequest != nil {
		return cfg.BuildRequest(cfg.SystemPrompt, messages, cfg.Tools)
	}
	return defaultBuildRequest(cfg, messages)
}

// defaultBuildRequest assembles system + user messages + tool definitions.
func defaultBuildRequest(cfg AgentLoopConfig, messages []ChatMessage) ChatRequest {
	req := ChatRequest{
		Model:    cfg.Model,
		Messages: make([]ChatMessage, 0, 1+len(messages)),
	}

	// System prompt
	if cfg.SystemPrompt != "" {
		req.Messages = append(req.Messages, ChatMessage{
			Role:    RoleSystem,
			Content: cfg.SystemPrompt,
		})
	}

	// Conversation messages
	req.Messages = append(req.Messages, messages...)

	// Tool definitions
	if cfg.Tools != nil {
		req.Tools = cfg.Tools.ToolDefs()
	}

	return req
}

// resolveProvider picks the right LLMProvider from the engine.
// If the LLM implements ModelRouter (e.g. RoutedLLMProvider) and a model
// name is known, routing is applied automatically via duck typing.
// No separate Router field is needed on the Engine.
func resolveProvider(engine *Engine, model string) (LLMProvider, error) {
	if !engine.HasLLM() {
		return nil, fmt.Errorf("no LLM provider configured — set WithLLM()")
	}
	if model != "" {
		if router, ok := engine.LLM.(ModelRouter); ok {
			return router.Route(model)
		}
	}
	return engine.LLM, nil
}

// callWithRetry calls the LLM and retries on transient errors with
// exponential backoff. Retry classification:
//   - Rate limit / 429 / "rate limit"  → retry with backoff
//   - Network / timeout / 5xx / "EOF"   → retry with backoff
//   - Auth / 401 / 403 / "unauthorized" → fail immediately
//   - Validation / 400 / "invalid"      → fail immediately
//   - Unknown                           → consult OnError hook
//
// Backoff: 1s, 2s, 4s, 8s (capped at 30s).
func callWithRetry(ctx context.Context, provider LLMProvider, req ChatRequest, maxRetries int, onError func(error, int) bool) (*ChatResponse, error) {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err := provider.Chat(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		switch classifyError(err) {
		case errClassPermanent:
			// Don't retry. Fail fast.
			return nil, err
		case errClassTransient:
			// Retry automatically with backoff.
			if attempt < maxRetries {
				delay := backoffDelay(attempt)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(delay):
				}
				continue
			}
		default: // errClassUnknown
			// Consult hook.
			if onError == nil || !onError(err, attempt) {
				return nil, err
			}
		}
	}
	return nil, fmt.Errorf("max retries (%d) exceeded: %w", maxRetries, lastErr)
}

type errClass int

const (
	errClassUnknown errClass = iota
	errClassTransient
	errClassPermanent
)

func classifyError(err error) errClass {
	if err == nil {
		return errClassUnknown
	}
	msg := strings.ToLower(err.Error())

	// Permanent errors — never retry
	permanent := []string{
		"unauthorized", "401", "403",
		"forbidden",
		"invalid api key", "authentication",
		"invalid request", "400",
		"not found", "404",
		"context length", "context window",
		"content_filter", "content filter",
	}
	for _, p := range permanent {
		if strings.Contains(msg, p) {
			return errClassPermanent
		}
	}

	// Transient errors — retry with backoff
	transient := []string{
		"rate limit", "429",
		"too many requests",
		"timeout", "deadline",
		"connection reset", "connection refused",
		"eof", "broken pipe",
		"500", "502", "503", "504",
		"server error", "service unavailable",
		"temporary",
	}
	for _, t := range transient {
		if strings.Contains(msg, t) {
			return errClassTransient
		}
	}

	return errClassUnknown
}

func backoffDelay(attempt int) time.Duration {
	// 1s, 2s, 4s, 8s, 16s, 30s (cap)
	d := time.Duration(1<<uint(attempt-1)) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}
