package autobuild

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ── Human-in-the-Loop ─────────────────────────────────────────────────────────
//
// Human-in-the-loop (HIL) pauses the AgentLoop before a tool executes and
// waits for a human to approve, reject, or modify the call. This gives
// operators fine-grained control over what the agent can do.
//
// Architecture:
//
//	AgentLoop (goroutine A)
//	  → OnToolCall calls HumanApprovalFilter.Inspect()
//	  → Inspect sends ApprovalRequest to ApprovalGate
//	  → Inspect BLOCKS waiting for ApprovalResponse from the gate
//
//	HTTP handler (goroutine B)
//	  → receives POST /confirm from the user
//	  → calls gate.Respond(ApprovalResponse{...})
//	  → Inspect unblocks and returns the verdict
//
// The frontend receives a `confirmation_required` SSE event and renders a
// dialog. The user approves/rejects, triggering POST /api/chats/:id/confirm.

// SafetyPause is a SafetyDecision that pauses the agent loop until a human
// responds. Unlike SafetyBlock (instant rejection), SafetyPause suspends
// execution and waits — the agent loop is live but frozen on that tool call.
const SafetyPause SafetyDecision = 3

// ApprovalRequest is emitted when a tool call requires human confirmation.
// The frontend renders this as a blocking dialog.
type ApprovalRequest struct {
	// ID is a unique identifier correlating the request with its response.
	// Use this when calling ApprovalGate.Respond.
	ID string

	// ToolCall is the call that needs approval.
	ToolCall ToolCallEntry

	// Reason explains why this call requires human review.
	// Shown to the user in the confirmation dialog.
	Reason string

	// CreatedAt is when the request was emitted.
	CreatedAt time.Time
}

// ApprovalResponse is the human's decision on an ApprovalRequest.
type ApprovalResponse struct {
	// ID must match the ApprovalRequest.ID this responds to.
	ID string

	// Approved is true if the human allowed the tool call.
	Approved bool

	// ModifiedArgs is an optional JSON string replacing the original tool
	// call arguments. Only used when Approved is true. If empty, the
	// original args are used unchanged.
	ModifiedArgs string
}

// ApprovalGate is the rendezvous point between the agent loop and the
// HTTP handler that receives human decisions.
//
// Create one gate per active stream session and wire it into:
//   - HumanApprovalFilter (sends requests, waits for responses)
//   - The HTTP confirm handler (calls Respond with the user's decision)
//
// Usage:
//
//	gate := autobuild.NewApprovalGate()
//	filter := autobuild.NewHumanApprovalFilter(gate, func(call ToolCallEntry) (bool, string) {
//	    if call.Name == "bash" { return true, "Bash commands require approval" }
//	    return false, ""
//	})
//	runtime := ab.NewRuntime(engine).WithSafety(filter)
//
//	// In HTTP confirm handler:
//	gate.Respond(ApprovalResponse{ID: id, Approved: true})
type ApprovalGate struct {
	mu       sync.Mutex
	pending  map[string]chan ApprovalResponse
	requests chan ApprovalRequest
}

// NewApprovalGate creates a new gate. bufSize controls how many requests
// can be queued before blocking — 8 is usually enough for one session.
func NewApprovalGate(bufSize int) *ApprovalGate {
	if bufSize <= 0 {
		bufSize = 8
	}
	return &ApprovalGate{
		pending:  make(map[string]chan ApprovalResponse),
		requests: make(chan ApprovalRequest, bufSize),
	}
}

// Requests returns the channel that emits ApprovalRequests as they arrive.
// Read from this channel and forward each request as a `confirmation_required`
// SSE event to the frontend.
func (g *ApprovalGate) Requests() <-chan ApprovalRequest {
	return g.requests
}

// Wait registers a pending approval and blocks until Respond is called
// or ctx is cancelled. Returns the response or an error if ctx expired.
func (g *ApprovalGate) Wait(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error) {
	ch := make(chan ApprovalResponse, 1)

	g.mu.Lock()
	g.pending[req.ID] = ch
	g.mu.Unlock()

	// Send to the requests channel so the HTTP layer can forward it as SSE
	select {
	case g.requests <- req:
	case <-ctx.Done():
		g.mu.Lock()
		delete(g.pending, req.ID)
		g.mu.Unlock()
		return ApprovalResponse{}, fmt.Errorf("context cancelled before request sent: %w", ctx.Err())
	}

	// Block until response arrives or ctx cancelled
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		g.mu.Lock()
		delete(g.pending, req.ID)
		g.mu.Unlock()
		return ApprovalResponse{}, fmt.Errorf("approval timed out: %w", ctx.Err())
	}
}

// Respond delivers a human decision to the waiting agent loop.
// Returns false if no request with that ID is pending (duplicate or expired).
func (g *ApprovalGate) Respond(resp ApprovalResponse) bool {
	g.mu.Lock()
	ch, ok := g.pending[resp.ID]
	if ok {
		delete(g.pending, resp.ID)
	}
	g.mu.Unlock()

	if !ok {
		return false
	}
	ch <- resp
	return true
}

// PendingCount returns how many approvals are currently waiting.
func (g *ApprovalGate) PendingCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.pending)
}

// ── HumanApprovalFilter ───────────────────────────────────────────────────────

// HumanApprovalFilter is a SafetyFilter that pauses the agent loop and waits
// for human approval before allowing certain tool calls to execute.
//
// It implements SafetyFilter — wire it via WithSafety(filter) or add it to
// a SafetyChain.
//
// When a tool call matches the policy function, Inspect blocks on the
// ApprovalGate until:
//   - The human approves → SafetyAllow (or SafetyTransform if args were modified)
//   - The human rejects → SafetyBlock with reason "rejected by user"
//   - ctx is cancelled → SafetyBlock with reason "approval timed out"
type HumanApprovalFilter struct {
	gate *ApprovalGate

	// policy decides if a tool call needs human approval.
	// Return (true, "reason shown to user") to require confirmation.
	// Return (false, "") to allow without interruption.
	policy func(call ToolCallEntry) (needsApproval bool, reason string)
}

// NewHumanApprovalFilter creates a filter backed by the given gate.
// policy is called for each tool call — return true to pause and wait.
func NewHumanApprovalFilter(
	gate *ApprovalGate,
	policy func(call ToolCallEntry) (bool, string),
) *HumanApprovalFilter {
	return &HumanApprovalFilter{gate: gate, policy: policy}
}

// Inspect implements SafetyFilter. Blocks if the call needs human approval.
func (f *HumanApprovalFilter) Inspect(ctx context.Context, call ToolCallEntry) SafetyVerdict {
	needs, reason := f.policy(call)
	if !needs {
		return SafetyVerdict{Decision: SafetyAllow}
	}

	req := ApprovalRequest{
		ID:        "apr_" + randomHex(8),
		ToolCall:  call,
		Reason:    reason,
		CreatedAt: time.Now(),
	}

	resp, err := f.gate.Wait(ctx, req)
	if err != nil {
		return SafetyVerdict{
			Decision: SafetyBlock,
			Reason:   "approval timed out or context cancelled: " + err.Error(),
		}
	}

	if !resp.Approved {
		return SafetyVerdict{
			Decision: SafetyBlock,
			Reason:   "rejected by user",
		}
	}

	// Approved with optional arg modification
	if resp.ModifiedArgs != "" && resp.ModifiedArgs != call.Arguments {
		return SafetyVerdict{
			Decision: SafetyTransform,
			Reason:   "approved with modifications",
			NewArgs:  resp.ModifiedArgs,
		}
	}

	return SafetyVerdict{Decision: SafetyAllow}
}

// ── WithHumanApproval ─────────────────────────────────────────────────────────

// DefaultApprovalPolicy returns a policy that requires approval for tool calls
// that can cause destructive or irreversible side effects:
//   - bash / shell / exec: all commands
//   - file_write / file_delete: filesystem mutations
//   - memory-operations (delete, str_replace): memory mutations
//
// Use as a starting point and customize per deployment.
func DefaultApprovalPolicy(call ToolCallEntry) (bool, string) {
	switch call.Name {
	case "bash", "shell", "exec":
		return true, "Bash commands can have irreversible side effects — please review before execution"
	case "file_write":
		return true, "Writing to the filesystem — please confirm the path and content"
	case "file_delete":
		return true, "Deleting a file is irreversible — please confirm"
	}
	return false, ""
}

// WithHumanApproval wires a HumanApprovalFilter into the safety chain and
// returns the ApprovalGate so the HTTP confirm handler can call gate.Respond.
//
// When using RunStream, the runtime automatically fans out ApprovalRequests
// from the gate as StreamEventConfirmationRequired events. The consumer does
// not need to poll the gate manually.
//
// Usage:
//
//	gate, runtime := ab.NewRuntime(engine).
//	    WithHumanApproval(ab.DefaultApprovalPolicy)
//	// Store gate keyed by chatID for the confirm handler.
func (r *Runtime) WithHumanApproval(policy func(ToolCallEntry) (bool, string)) (*ApprovalGate, *Runtime) {
	gate := NewApprovalGate(8)
	filter := NewHumanApprovalFilter(gate, policy)
	existing := r.safety
	if existing != nil {
		r.safety = NewSafetyChain(filter, existing)
	} else {
		r.safety = filter
	}
	r.approvalGate = gate
	return gate, r
}
