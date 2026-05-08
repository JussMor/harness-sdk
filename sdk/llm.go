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
	Name       string          `json:"name,omitempty"`         // tool name when Role == RoleTool
	ToolCallID string          `json:"tool_call_id,omitempty"` // links a tool result to a request
	ToolCalls  []ToolCallEntry `json:"tool_calls,omitempty"`   // assistant requesting tool calls
	Images     []ImageContent  `json:"images,omitempty"`       // attached images (vision-enabled models)
	Documents  []DocumentContent `json:"documents,omitempty"`  // attached PDFs / text documents
}

// ImageContent represents an image attached to a chat message.
type ImageContent struct {
	Source    string `json:"source,omitempty"`     // base64-encoded data (without data: prefix)
	MediaType string `json:"media_type,omitempty"` // "image/jpeg", "image/png", "image/webp", "image/gif"
	URL       string `json:"url,omitempty"`        // alternative to Source for URL-based images
}

// DocumentContent represents a PDF or text document attached to a chat message.
// Anthropic supports this via type=document content blocks.
type DocumentContent struct {
	// Source is the base64-encoded document data.
	Source string `json:"source,omitempty"`

	// MediaType is the MIME type. Supported: "application/pdf", "text/plain".
	MediaType string `json:"media_type,omitempty"`

	// URL is an alternative to Source for URL-accessible documents.
	URL string `json:"url,omitempty"`

	// Title is an optional human-readable label shown in the context.
	Title string `json:"title,omitempty"`

	// CacheControl enables prompt caching for this document.
	// Set to "ephemeral" to cache. Useful for large PDFs reused across turns.
	CacheControl string `json:"cache_control,omitempty"`
}

// ToolCallEntry represents a single tool call requested by the LLM.
type ToolCallEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// ToolChoice controls how the LLM selects tools.
// When empty, the model decides automatically.
type ToolChoice struct {
	// Type is one of: "auto" (model decides), "any" (must use a tool),
	// "tool" (must use the specific tool named in Name).
	Type string `json:"type"`

	// Name is required when Type == "tool".
	Name string `json:"name,omitempty"`

	// DisableParallelToolUse prevents the model from calling multiple tools
	// in a single response. Only honored by providers that support it.
	DisableParallelToolUse bool `json:"disable_parallel_tool_use,omitempty"`
}

// ChatRequest is sent to an LLM provider.
type ChatRequest struct {
	// Model is the model identifier (e.g. "claude-sonnet-4-20250514", "gpt-4o").
	// If empty, the provider uses its own default.
	Model string `json:"model,omitempty"`

	// Messages is the conversation history.
	Messages []ChatMessage `json:"messages"`

	// Tools are the tool definitions available for the LLM to call.
	Tools []ToolDef `json:"tools,omitempty"`

	// ToolChoice controls how the LLM selects tools.
	// Zero value = auto (model decides). Set Type="any" to force tool use,
	// or Type="tool" + Name="bash" to force a specific tool.
	ToolChoice *ToolChoice `json:"tool_choice,omitempty"`

	// Temperature controls randomness. 0 = deterministic.
	Temperature float64 `json:"temperature,omitempty"`

	// TopP is nucleus sampling probability (0–1). Lower = more focused.
	// Most providers honor either Temperature or TopP, not both.
	TopP float64 `json:"top_p,omitempty"`

	// TopK limits the vocabulary to the top-K tokens at each step.
	// Supported by Anthropic and some other providers.
	TopK int `json:"top_k,omitempty"`

	// MaxTokens caps the response length.
	MaxTokens int `json:"max_tokens,omitempty"`

	// ReasoningEffort hints the model to use more/less reasoning.
	// Supported values: "low", "medium", "high". Not all providers honor this.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	// ThinkingBudget is the maximum number of tokens the model can spend
	// on internal reasoning. Only Claude 3.7+ honors this.
	// MaxTokens must be greater than ThinkingBudget.
	ThinkingBudget int `json:"thinking_budget,omitempty"`

	// Stop sequences that terminate generation.
	Stop []string `json:"stop,omitempty"`

	// Metadata carries provider-specific extra fields.
	// For Anthropic: {"user_id": "..."} for abuse tracking.
	// For OpenAI: {"user": "..."}.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ChatResponse is returned by an LLM provider.
type ChatResponse struct {
	// Content is the text response (may be empty if the model chose tool calls).
	Content string `json:"content"`

	// Reasoning is optional provider-exposed reasoning content when the vendor
	// explicitly returns it. This is not synthesized by the SDK.
	Reasoning string `json:"reasoning,omitempty"`

	// ThinkingContent holds the model's internal extended thinking content,
	// if the request enabled ThinkingBudget. This is the raw thinking text
	// returned in "thinking" content blocks by the Anthropic API.
	// Not shown to users by default — use for debugging and tracing only.
	ThinkingContent string `json:"thinking_content,omitempty"`

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
