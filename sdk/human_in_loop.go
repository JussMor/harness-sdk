package autobuild

import (
	"context"
	"sync"
	"time"
)

// ── Human-in-the-Loop (legacy facade over InterruptGate) ─────────────────────
//
// Human-in-the-loop (HIL) pauses the AgentLoop before a tool executes and
// waits for a human to approve, reject, or modify the call.
//
// As of v1 of the protocol, HIL is one variant of the broader Interrupt
// system (see interrupt.go: InterruptGate, InterruptKindApproval).
// ApprovalGate, ApprovalRequest, and ApprovalResponse are kept as a
// backwards-compatible facade: the Runtime, the example backend, and any
// downstream consumers continue to compile unchanged. New code should
// prefer *InterruptGate directly — it supports questions and form input
// in addition to tool approvals.

// SafetyPause is a SafetyDecision that pauses the agent loop until a human
// responds. Unlike SafetyBlock (instant rejection), SafetyPause suspends
// execution and waits — the agent loop is live but frozen on that tool call.
const SafetyPause SafetyDecision = 3

// ApprovalRequest is the legacy shape for tool-approval interrupts. It maps
// 1:1 to InterruptRequest{Kind: InterruptKindApproval, Approval: ...}.
type ApprovalRequest struct {
	ID        string
	ToolCall  ToolCallEntry
	Reason    string
	CreatedAt time.Time
}

// ApprovalResponse is the legacy shape for tool-approval responses. It maps
// to InterruptResponse fields (ID, Approved, ModifiedArgs).
type ApprovalResponse struct {
	ID           string
	Approved     bool
	ModifiedArgs string
}

// ApprovalGate is a backwards-compatible facade over *InterruptGate scoped
// to InterruptKindApproval requests. New code should use *InterruptGate
// directly via Runtime.WithInterrupts.
type ApprovalGate struct {
	inner *InterruptGate

	bridgeOnce sync.Once
	bridgeCh   chan ApprovalRequest
}

// NewApprovalGate creates an approval-only gate.
func NewApprovalGate(bufSize int) *ApprovalGate {
	return &ApprovalGate{inner: NewInterruptGate(bufSize)}
}

// Inner returns the underlying *InterruptGate. Use this to add non-approval
// interrupts (questions, form input) without creating a second gate.
func (a *ApprovalGate) Inner() *InterruptGate { return a.inner }

// Requests returns a channel of ApprovalRequests. Only InterruptKindApproval
// requests raised on the inner gate are forwarded; questions and form-input
// interrupts are ignored here — consume those via Inner().Requests().
//
// NOTE: starting the bridge consumes the inner gate's Requests() channel.
// Do not also iterate Inner().Requests() concurrently — pick one.
func (a *ApprovalGate) Requests() <-chan ApprovalRequest {
	a.bridgeOnce.Do(func() {
		a.bridgeCh = make(chan ApprovalRequest, cap(a.inner.requests))
		go func() {
			defer close(a.bridgeCh)
			for req := range a.inner.requests {
				if req.Kind != InterruptKindApproval || req.Approval == nil {
					continue
				}
				a.bridgeCh <- ApprovalRequest{
					ID:        req.ID,
					ToolCall:  req.Approval.ToolCall,
					Reason:    req.Reason,
					CreatedAt: req.CreatedAt,
				}
			}
		}()
	})
	return a.bridgeCh
}

// Wait registers a pending approval and blocks until Respond is called or
// ctx is cancelled.
func (a *ApprovalGate) Wait(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error) {
	intReq := InterruptRequest{
		ID:        req.ID,
		Kind:      InterruptKindApproval,
		Reason:    req.Reason,
		CreatedAt: req.CreatedAt,
		Approval:  &ApprovalPayload{ToolCall: req.ToolCall},
	}
	intResp, err := a.inner.Wait(ctx, intReq)
	if err != nil {
		return ApprovalResponse{}, err
	}
	return ApprovalResponse{
		ID:           intResp.ID,
		Approved:     intResp.Approved,
		ModifiedArgs: intResp.ModifiedArgs,
	}, nil
}

// Respond delivers a human decision to the waiting agent loop.
// Returns false if no request with that ID is pending.
func (a *ApprovalGate) Respond(resp ApprovalResponse) bool {
	return a.inner.Respond(InterruptResponse{
		ID:           resp.ID,
		Approved:     resp.Approved,
		ModifiedArgs: resp.ModifiedArgs,
	})
}

// PendingCount returns how many approvals are currently waiting.
func (a *ApprovalGate) PendingCount() int { return a.inner.PendingCount() }

// IssueResolutionToken mints a signed token for inbound webhook resolution
// of the named approval. See InterruptGate.IssueResolutionToken.
func (a *ApprovalGate) IssueResolutionToken(id string, ttl time.Duration) (string, error) {
	return a.inner.IssueResolutionToken(id, ttl)
}

// ResolveByToken redeems a token previously returned by IssueResolutionToken.
func (a *ApprovalGate) ResolveByToken(token string, resp ApprovalResponse) error {
	return a.inner.ResolveByToken(token, InterruptResponse{
		Approved:     resp.Approved,
		ModifiedArgs: resp.ModifiedArgs,
	})
}

// ── HumanApprovalFilter ───────────────────────────────────────────────────────

// HumanApprovalFilter is a SafetyFilter that pauses the agent loop and waits
// for human approval before allowing certain tool calls to execute.
type HumanApprovalFilter struct {
	gate   *ApprovalGate
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
		ID:        newInterruptID(InterruptKindApproval),
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
	if resp.ModifiedArgs != "" && resp.ModifiedArgs != call.Arguments {
		return SafetyVerdict{
			Decision: SafetyTransform,
			Reason:   "approved with modifications",
			NewArgs:  resp.ModifiedArgs,
		}
	}
	return SafetyVerdict{Decision: SafetyAllow}
}

// ── Default policy ───────────────────────────────────────────────────────────

// DefaultApprovalPolicy returns a policy that requires approval for tool calls
// that can cause destructive or irreversible side effects:
//   - bash / shell / exec: all commands
//   - file_write / file_delete: filesystem mutations
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

// ── Runtime wiring ───────────────────────────────────────────────────────────

// WithHumanApproval wires a HumanApprovalFilter into the safety chain and
// returns the ApprovalGate so the HTTP confirm handler can call gate.Respond.
//
// When using RunStream, the runtime automatically fans out ApprovalRequests
// from the gate as StreamEventConfirmationRequired (legacy) and
// StreamEventInterruptRequired events.
func (r *Runtime) WithHumanApproval(policy func(ToolCallEntry) (bool, string)) (*ApprovalGate, *Runtime) {
	gate := NewApprovalGate(8)
	filter := NewHumanApprovalFilter(gate, policy)
	existing := r.safety
	if existing != nil {
		r.safety = NewSafetyChain(filter, existing)
	} else {
		r.safety = filter
	}
	r.interruptGate = gate.inner
	return gate, r
}

// WithInterrupts attaches a generalized InterruptGate to the runtime. When
// set, runtime fans out interrupt requests on the streaming channel.
//
// If a HumanApprovalFilter is also configured (via WithHumanApproval), pass
// gate.Inner() here so both subsystems share the same gate.
func (r *Runtime) WithInterrupts(gate *InterruptGate) *Runtime {
	r.interruptGate = gate
	return r
}

// InterruptGate returns the gate currently attached to the runtime, or nil.
func (r *Runtime) InterruptGate() *InterruptGate { return r.interruptGate }
