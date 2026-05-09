package autobuild

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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

	// Pre-execution validation. Lets the tool reject bad input with a clear
	// message that the LLM can act on (e.g. "path must be absolute").
	if tool.Validate != nil {
		if err := tool.Validate(ctx, args); err != nil {
			return ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				Content:    fmt.Sprintf("error: %v", err),
				Error:      err,
			}
		}
	}

	// Permission gate. Denials surface to the LLM as a tool error so it can
	// adapt; ask_user is treated as a deny at this layer (front-ends that
	// want interactive confirmation should intercept before Dispatch).
	if tool.CheckPermissions != nil {
		decision, err := tool.CheckPermissions(ctx, args)
		if err != nil {
			return ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				Content:    fmt.Sprintf("error: permission check failed: %v", err),
				Error:      err,
			}
		}
		switch decision.Decision {
		case PermissionDeny, PermissionAskUser:
			reason := strings.TrimSpace(decision.Reason)
			if reason == "" {
				reason = "permission denied"
			}
			return ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				Content:    fmt.Sprintf("error: %s", reason),
				Error:      fmt.Errorf("%s", reason),
			}
		case PermissionAllow, "":
			if decision.UpdatedArgs != nil {
				args = decision.UpdatedArgs
			}
		}
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

// DispatchAll executes all tool calls sequentially and returns results in order.
// Use when tool calls are dependent on each other.
func (d *ToolDispatcher) DispatchAll(ctx context.Context, calls []ToolCallEntry, sandboxID string) []ToolResult {
	results := make([]ToolResult, len(calls))
	for i, call := range calls {
		results[i] = d.Dispatch(ctx, call, sandboxID)
	}
	return results
}

// DispatchParallel executes all tool calls concurrently and returns results
// in the same order as the input. Use when tool calls are independent —
// the result of one does not feed the parameters of another.
//
// This mirrors how Claude executes tools: independent calls fire together,
// dependent calls serialize. The caller decides which calls are independent
// (typically the LLM, but a static analyzer could too).
//
// Tools whose IsConcurrencySafe predicate returns false are executed serially
// AFTER the parallel batch finishes, preserving order in the output slice.
// This protects tools that touch shared mutable state (e.g. memory writes,
// sandbox file edits) from racing with each other.
//
// All goroutines share the parent context. If ctx is cancelled, in-flight
// tools see the cancellation but already-returned results are preserved.
func (d *ToolDispatcher) DispatchParallel(ctx context.Context, calls []ToolCallEntry, sandboxID string) []ToolResult {
	if len(calls) == 0 {
		return nil
	}
	if len(calls) == 1 {
		return []ToolResult{d.Dispatch(ctx, calls[0], sandboxID)}
	}

	results := make([]ToolResult, len(calls))

	// Partition by ConcurrencySafe.
	type indexedCall struct {
		idx  int
		call ToolCallEntry
	}
	var safe, unsafe []indexedCall
	for i, c := range calls {
		t := d.tools.Get(c.Name)
		isSafe := false
		if t != nil {
			var parsed map[string]any
			if c.Arguments != "" && c.Arguments != "{}" {
				_ = json.Unmarshal([]byte(c.Arguments), &parsed)
			}
			isSafe = t.ConcurrencySafe(parsed)
		}
		if isSafe {
			safe = append(safe, indexedCall{i, c})
		} else {
			unsafe = append(unsafe, indexedCall{i, c})
		}
	}

	var wg sync.WaitGroup
	wg.Add(len(safe))
	for _, ic := range safe {
		go func(ic indexedCall) {
			defer wg.Done()
			results[ic.idx] = d.Dispatch(ctx, ic.call, sandboxID)
		}(ic)
	}
	wg.Wait()

	for _, ic := range unsafe {
		results[ic.idx] = d.Dispatch(ctx, ic.call, sandboxID)
	}
	return results
}

// AreIndependent returns true when none of the calls reference values
// that another call would produce. This is a heuristic — final independence
// determination should come from the LLM. Useful as a static fallback
// when the LLM does not classify its own calls.
//
// Independent here means: no call's argument JSON contains a value that
// looks like a placeholder for another call's output (e.g. "${call_1.result}").
func AreIndependent(calls []ToolCallEntry) bool {
	if len(calls) <= 1 {
		return true
	}
	// Look for placeholder patterns "${...}" or "{{...}}" in arguments.
	for _, c := range calls {
		if strings.Contains(c.Arguments, "${") || strings.Contains(c.Arguments, "{{") {
			return false
		}
	}
	return true
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
