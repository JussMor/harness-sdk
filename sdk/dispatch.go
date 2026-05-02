package autobuild

import (
	"context"
	"encoding/json"
	"fmt"
)

// ToolDispatcher resolves tool calls from the LLM into actual executions.
// It parses the JSON arguments, finds the tool in the registry, executes it,
// and returns the result as a string ready to be fed back to the LLM.
type ToolDispatcher struct {
	tools   *ToolRegistry
	sandbox SandboxDriver
}

// NewToolDispatcher creates a dispatcher bound to a tool registry and
// an optional sandbox (for tools that need a sandboxID).
func NewToolDispatcher(tools *ToolRegistry, sandbox SandboxDriver) *ToolDispatcher {
	return &ToolDispatcher{tools: tools, sandbox: sandbox}
}

// ToolResult holds the outcome of dispatching a single tool call.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	Error      error  `json:"-"`
}

// Dispatch executes a single tool call and returns the result.
// If the tool is not found or has no Execute function, it returns an error
// message as content (the LLM needs to see errors to self-correct).
func (d *ToolDispatcher) Dispatch(ctx context.Context, call ToolCallEntry, sandboxID string) ToolResult {
	tool := d.tools.Get(call.Name)
	if tool == nil {
		return ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    fmt.Sprintf("error: tool %q not found in registry", call.Name),
			Error:      fmt.Errorf("tool %q not found", call.Name),
		}
	}

	if tool.Execute == nil {
		return ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    fmt.Sprintf("error: tool %q has no execute function", call.Name),
			Error:      fmt.Errorf("tool %q not executable", call.Name),
		}
	}

	// Parse JSON arguments into map
	var args map[string]any
	if call.Arguments != "" && call.Arguments != "{}" {
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				Content:    fmt.Sprintf("error: invalid JSON arguments: %v", err),
				Error:      err,
			}
		}
	}
	if args == nil {
		args = make(map[string]any)
	}

	// Execute
	result, err := tool.Execute(ctx, sandboxID, args)
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    fmt.Sprintf("error: %v", err),
			Error:      err,
		}
	}

	return ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    result,
	}
}

// DispatchAll executes all tool calls from a ChatResponse sequentially
// and returns results in order. For parallel execution, the caller can
// use goroutines and call Dispatch individually.
func (d *ToolDispatcher) DispatchAll(ctx context.Context, calls []ToolCallEntry, sandboxID string) []ToolResult {
	results := make([]ToolResult, len(calls))
	for i, call := range calls {
		results[i] = d.Dispatch(ctx, call, sandboxID)
	}
	return results
}

// ToMessages converts tool results into ChatMessages ready to append
// to the conversation history. Includes the assistant's tool_calls message
// and all individual tool results.
func ToMessages(assistantToolCalls []ToolCallEntry, results []ToolResult) []ChatMessage {
	msgs := make([]ChatMessage, 0, 1+len(results))

	// The assistant message that requested these tool calls
	msgs = append(msgs, ChatMessage{
		Role:      RoleAssistant,
		ToolCalls: assistantToolCalls,
	})

	// Each tool result
	for _, r := range results {
		msgs = append(msgs, ChatMessage{
			Role:       RoleTool,
			Name:       r.Name,
			ToolCallID: r.ToolCallID,
			Content:    r.Content,
		})
	}

	return msgs
}
