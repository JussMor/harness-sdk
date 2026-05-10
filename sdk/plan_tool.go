package autobuild

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════
// plan_tool — EnterPlanMode / ExitPlanMode (Claude Code parity)
// ═══════════════════════════════════════════════════════════════════════
//
// Mirrors Claude Code's plan-mode flow:
//
//   1. EnterPlanMode → flips PermissionEngine to PermissionModePlan, which
//      blocks every non-read-only tool. Read tools (Glob, Grep, file_read,
//      memory.view, …) keep working so the agent can explore.
//   2. The agent writes a plan to a designated path/buffer.
//   3. ExitPlanMode → emits an InterruptKindApproval to the user. On
//      approval, the engine returns to its previous mode. On rejection,
//      the agent stays in plan mode.
//
// The two tools share state via PlanController, which is the runtime
// handle a host (backend) keeps to inspect / drive plan-mode transitions
// from outside the LLM (e.g. user clicks "Exit Plan Mode" in the UI).
//
// References:
//   docs/Claude Code/tools/EnterPlanModeTool/{prompt.ts, EnterPlanModeTool.ts}
//   docs/Claude Code/tools/ExitPlanModeTool/{prompt.ts, ExitPlanModeV2Tool.ts}

// ── Controller ───────────────────────────────────────────────────────────────

// PlanController holds the shared state for a session's plan mode. Construct
// one per Runtime and pass it to NewPlanTool. Safe for concurrent use.
type PlanController struct {
	mu sync.Mutex

	permissions *PermissionEngine
	prevMode    PermissionMode
	inPlan      bool

	// Plan text most recently written by the agent (or seeded by the host).
	plan      string
	planAt    time.Time

	// Optional callback fired when the user approves an exit. The runtime/host
	// can use this to persist the plan, surface it in the UI, or kick off
	// the implementation phase.
	OnApproved func(ctx context.Context, plan string)

	// Optional callback fired when the user rejects an exit. The agent stays
	// in plan mode; hosts may use this to surface the rejection reason.
	OnRejected func(ctx context.Context, reason string)

	// OnStateChanged fires after Enter/Exit. The host (runtime/backend) uses
	// this to publish StreamEventPlanModeChanged on the live SSE channel.
	OnStateChanged func(ev PlanModeEvent)
}

// NewPlanController returns a controller bound to a PermissionEngine. Engine
// may be nil — in that case mode toggling is a no-op (plan-mode semantics
// then collapse to "track plan text + ask user before continuing").
func NewPlanController(engine *PermissionEngine) *PlanController {
	return &PlanController{permissions: engine}
}

// SetPermissions binds (or rebinds) a PermissionEngine after construction.
// Use this when the engine is created later than the controller (e.g. in
// hosts that wire permissions per-request).
func (c *PlanController) SetPermissions(engine *PermissionEngine) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.permissions = engine
}

// InPlanMode reports whether the controller is currently in plan mode.
func (c *PlanController) InPlanMode() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inPlan
}

// Plan returns the most recently written plan text and its timestamp.
func (c *PlanController) Plan() (string, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.plan, c.planAt
}

// SetPlan stores plan text on the controller. Tools or hosts may call this
// directly (e.g. the agent invokes a separate "write_plan" mechanism).
func (c *PlanController) SetPlan(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.plan = s
	c.planAt = time.Now()
}

// Enter switches the engine to PermissionModePlan and remembers the prior
// mode for restoration. Idempotent — calling twice keeps the original
// prior mode.
func (c *PlanController) Enter() {
	c.mu.Lock()
	if c.inPlan {
		c.mu.Unlock()
		return
	}
	if c.permissions != nil {
		c.prevMode = c.permissions.Mode()
		c.permissions.SetMode(PermissionModePlan)
	}
	c.inPlan = true
	cb := c.OnStateChanged
	c.mu.Unlock()
	if cb != nil {
		cb(PlanModeEvent{State: "entered"})
	}
}

// Exit restores the previous permission mode (or "default" if none was
// captured). Returns the plan text snapshot so callers can persist it.
func (c *PlanController) Exit() string {
	c.mu.Lock()
	if c.permissions != nil && c.inPlan {
		restore := c.prevMode
		if restore == "" {
			restore = PermissionModeDefault
		}
		c.permissions.SetMode(restore)
	}
	c.inPlan = false
	plan := c.plan
	cb := c.OnStateChanged
	c.mu.Unlock()
	if cb != nil {
		cb(PlanModeEvent{State: "exited", Plan: plan})
	}
	return plan
}

// ── Tool prompts ─────────────────────────────────────────────────────────────

const enterPlanModeToolPrompt = `Use this tool BEFORE starting a non-trivial implementation task. Plan mode lets you explore the codebase and design an approach for the user to approve before any files change.

## When to use
- New features with multiple valid implementations
- Refactors touching more than 2-3 files
- Tasks where requirements need clarification
- Architectural decisions (auth, caching, real-time, state)

## When NOT to use
- One-line typo / obvious bug fix
- Single function with clear requirements
- Pure research ("what handles routing?") — use Agent / search instead
- The user gave specific, detailed instructions

## What happens
- The session permission mode flips to "plan": all read tools (search, view,
  memory.view, file_read) keep working; any tool that mutates state is denied.
- You should: explore, identify patterns, design an approach, then write the
  plan via the plan operation and call ExitPlanMode for user approval.

This tool requires no parameters.`

const exitPlanModeToolPrompt = `Use this tool when you have finished writing your plan and are ready for user approval.

## How it works
- Pass the plan text in the "plan" argument (markdown).
- The user is asked to approve. On approval, plan mode ends and the previous
  permission mode is restored — implementation can begin.
- On rejection, plan mode stays active. Iterate on the plan and call again.

## Before calling
- Resolve every open question first (use AskUserQuestion in earlier turns).
  Do NOT use this tool to ask "is this plan ok?" — that's exactly what it does.
- Only call this from plan mode. If you're not in plan mode, the call is rejected.

## Don't use for research
Pure exploration tasks ("understand vim mode") never need plan-mode approval.
Skip ExitPlanMode entirely for those.`

// ── Builders ────────────────────────────────────────────────────────────────

// PlanToolConfig configures the plan-mode tool pair.
type PlanToolConfig struct {
	// Controller is required — it owns mode state and the plan buffer.
	Controller *PlanController

	// AutoApprove skips the InterruptKindApproval call on Exit. Useful in
	// non-interactive contexts (eval harness, scripted runs).
	AutoApprove bool
}

// NewEnterPlanModeTool builds the EnterPlanMode tool.
func NewEnterPlanModeTool(cfg PlanToolConfig) *Tool {
	if cfg.Controller == nil {
		cfg.Controller = NewPlanController(nil)
	}
	return &Tool{
		Name:        "EnterPlanMode",
		Description: enterPlanModeToolPrompt,
		Category:    ToolCategoryPlanning,
		IsReadOnly:  func(map[string]any) bool { return true },
		Parameters: ToolFuncParams{
			Type:       "object",
			Properties: map[string]ToolParam{},
		},
		Execute: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			cfg.Controller.Enter()
			return "Entered plan mode. Explore the codebase, design an approach, then call ExitPlanMode with your plan for user approval. Read tools (search/view/grep) work; mutating tools (file_write, bash, memory.create, …) are blocked until the plan is approved.", nil
		},
	}
}

// NewExitPlanModeTool builds the ExitPlanMode tool.
func NewExitPlanModeTool(cfg PlanToolConfig) *Tool {
	if cfg.Controller == nil {
		cfg.Controller = NewPlanController(nil)
	}
	return &Tool{
		Name:        "ExitPlanMode",
		Description: exitPlanModeToolPrompt,
		Category:    ToolCategoryPlanning,
		IsReadOnly:  func(map[string]any) bool { return true }, // gating happens via interrupt, not perms
		Parameters: ToolFuncParams{
			Type: "object",
			Properties: map[string]ToolParam{
				"plan": {
					Type:        "string",
					Description: "Plan content in markdown. Must be complete and unambiguous; the user will approve or reject it.",
				},
			},
			Required: []string{"plan"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			if !cfg.Controller.InPlanMode() {
				return "", fmt.Errorf("not in plan mode — call EnterPlanMode first, or skip this tool for non-implementation tasks")
			}
			plan := strings.TrimSpace(asPlanString(args["plan"]))
			if plan == "" {
				return "", fmt.Errorf("plan text is required")
			}
			cfg.Controller.SetPlan(plan)

			// Auto-approve fast-path (eval / non-interactive).
			if cfg.AutoApprove {
				cfg.Controller.Exit()
				if cfg.Controller.OnApproved != nil {
					cfg.Controller.OnApproved(ctx, plan)
				}
				return "Plan auto-approved. Proceeding to implementation.", nil
			}

			// Ask the user via the session's InterruptGate.
			payload, _ := json.Marshal(map[string]any{
				"plan": plan,
			})
			req := InterruptRequest{
				Kind:   InterruptKindApproval,
				Reason: "Approve plan and exit plan mode?",
				Approval: &ApprovalPayload{
					ToolCall: ToolCallEntry{
						Name:      "ExitPlanMode",
						Arguments: string(payload),
					},
				},
			}
			resp, err := RequestInterrupt(ctx, req)
			if err != nil {
				// No interrupt gate installed (headless run). Treat as
				// implicit approval: exit plan mode, surface in result.
				if err == ErrNoInterruptGate {
					cfg.Controller.Exit()
					if cfg.Controller.OnApproved != nil {
						cfg.Controller.OnApproved(ctx, plan)
					}
					return "No interrupt gate — exited plan mode without explicit approval. Proceeding.", nil
				}
				return "", err
			}

			if !resp.Approved {
				if cfg.Controller.OnRejected != nil {
					cfg.Controller.OnRejected(ctx, string(resp.Answer))
				}
				return "Plan rejected. You remain in plan mode — iterate and call ExitPlanMode again. Reason: " + safeReason(resp.Answer), nil
			}

			cfg.Controller.Exit()
			if cfg.Controller.OnApproved != nil {
				cfg.Controller.OnApproved(ctx, plan)
			}
			return "Plan approved. Plan mode exited; previous permission mode restored. Begin implementation.", nil
		},
	}
}

// NewPlanTools returns the (EnterPlanMode, ExitPlanMode) pair sharing one
// controller. Convenience for callers that want both at once.
func NewPlanTools(cfg PlanToolConfig) (*Tool, *Tool) {
	if cfg.Controller == nil {
		cfg.Controller = NewPlanController(nil)
	}
	return NewEnterPlanModeTool(cfg), NewExitPlanModeTool(cfg)
}

func safeReason(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "(no reason provided)"
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return s
	}
	return string(raw)
}

func asPlanString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	}
	b, _ := json.Marshal(v)
	return string(b)
}
