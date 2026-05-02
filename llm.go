package autobuild

import "context"

// ═══════════════════════════════════════════════════════════════════════
// LLM provider — the abstraction for calling language models
// ═══════════════════════════════════════════════════════════════════════

// Role identifies a chat message participant.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ChatMessage is one turn in a conversation.
type ChatMessage struct {
	Role       Role            `json:"role"`
	Content    string          `json:"content"`
	Name       string          `json:"name,omitempty"`        // tool name when Role == RoleTool
	ToolCallID string          `json:"tool_call_id,omitempty"` // links a tool result to a request
	ToolCalls  []ToolCallEntry `json:"tool_calls,omitempty"`   // assistant requesting tool calls
}

// ToolCallEntry represents a single tool call requested by the LLM.
type ToolCallEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// ChatRequest is sent to an LLM provider.
type ChatRequest struct {
	// Model is the model identifier (e.g. "claude-sonnet-4-20250514", "gpt-4o").
	// If empty, the provider should use its default.
	Model string `json:"model,omitempty"`

	// Messages is the conversation history.
	Messages []ChatMessage `json:"messages"`

	// Tools are the tool definitions available for the LLM to call.
	// Populated from the ToolRegistry filtered by the active Mode.
	Tools []ToolDef `json:"tools,omitempty"`

	// Temperature controls randomness. 0 = deterministic.
	Temperature float64 `json:"temperature,omitempty"`

	// MaxTokens caps the response length.
	MaxTokens int `json:"max_tokens,omitempty"`

	// ReasoningEffort hints the model to use more/less reasoning.
	// Supported values: "low", "medium", "high". Not all providers honor this.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	// Stop sequences that terminate generation.
	Stop []string `json:"stop,omitempty"`
}

// ChatResponse is returned by an LLM provider.
type ChatResponse struct {
	// Content is the text response (may be empty if the model chose tool calls).
	Content string `json:"content"`

	// ToolCalls are tool invocations the LLM wants to execute.
	ToolCalls []ToolCallEntry `json:"tool_calls,omitempty"`

	// FinishReason indicates why the model stopped: "stop", "tool_calls",
	// "length", "content_filter".
	FinishReason string `json:"finish_reason"`

	// Usage tracks token consumption for accounting.
	Usage TokenUsage `json:"usage"`

	// Model is the actual model used (may differ from requested if aliased).
	Model string `json:"model,omitempty"`
}

// TokenUsage tracks token consumption for a single request.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// LLMProvider is the core abstraction for calling language models.
// Implement this interface to integrate any LLM vendor (Anthropic, OpenAI,
// Ollama, vLLM, Bedrock, etc.).
//
// The SDK never calls an LLM directly — all calls flow through this interface,
// making the choice of model a deployment decision, not a code decision.
//
// The typical flow:
//
//  1. Engine resolves Mode → gets ModelSettings (model, temperature, reasoning)
//  2. Engine builds ChatRequest with system prompt + messages + tool defs
//  3. Engine calls LLMProvider.Chat(ctx, request)
//  4. Engine processes ChatResponse (text or tool calls)
//  5. If tool calls → execute tools → append results → loop back to step 3
type LLMProvider interface {
	// Chat sends a conversation to the LLM and returns the response.
	// Implementations should handle retries, rate limiting, and streaming
	// internally. The caller sees a single synchronous response.
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// ═══════════════════════════════════════════════════════════════════════
// ModelRouter — optional multi-model routing
// ═══════════════════════════════════════════════════════════════════════

// ModelRouter resolves which LLMProvider handles a given model name.
// Use this when your system uses multiple LLM vendors simultaneously
// (e.g. Anthropic for reasoning, OpenAI for embeddings, Ollama for local).
type ModelRouter interface {
	// Route returns the LLMProvider for the given model name.
	Route(model string) (LLMProvider, error)
}

// ═══════════════════════════════════════════════════════════════════════
// Helpers — building a ChatRequest from Engine state
// ═══════════════════════════════════════════════════════════════════════

// NewChatRequest builds a ChatRequest from a Mode's settings and the Engine's
// tool registry, filtered by the mode's allow/deny list.
func NewChatRequest(mode *Mode, systemPrompt string, messages []ChatMessage, tools *ToolRegistry) ChatRequest {
	req := ChatRequest{
		Messages: append([]ChatMessage{{Role: RoleSystem, Content: systemPrompt}}, messages...),
	}

	// Apply model settings from the mode
	if mode.ModelSettings != nil {
		req.Model = mode.ModelSettings.Model
		req.Temperature = mode.ModelSettings.Temperature
		req.ReasoningEffort = mode.ModelSettings.ReasoningEffort
	}

	// Filter tools by mode access control
	if tools != nil {
		for _, tool := range tools.List() {
			if mode.IsToolAllowed(tool.Name) {
				req.Tools = append(req.Tools, tool.ToToolDef())
			}
		}
	}

	return req
}
