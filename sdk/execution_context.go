package autobuild

import "context"

// ExecutionContext is the single source of truth for what the agent is doing
// right now. It fuses what were previously three separate concerns:
//
//   - WorkflowEngine  — which phase the conversation is in
//   - PlanProvider    — what structured work exists (DAG of steps)
//   - Todos           — the live checklist for the current phase
//
// In practice these three always move together: you advance a phase,
// you propose a plan, you update todos. Having them as separate providers
// forced the caller to coordinate them manually. ExecutionContext makes
// that coordination the SDK's responsibility.
//
// Lifecycle:
//
//	Orientation → Alignment → Preparation → Execution → Verification → Closure
//	                              ↑ plan lives here, todos track progress ↑
type ExecutionContext interface {
	// ── Phase ────────────────────────────────────────────────────

	// Phase returns the active lifecycle phase.
	Phase() Phase

	// Advance moves to the next phase in sequence.
	// Hooks registered for the target phase are called first.
	Advance(ctx context.Context) error

	// SetPhase jumps to a specific phase (e.g. retry Execution after Verification fails).
	SetPhase(ctx context.Context, p Phase) error

	// Attempt returns how many times the current phase has been entered.
	// Starts at 1. Increments on SetPhase to same phase or retry.
	Attempt() int

	// RegisterHook adds a callback invoked on entry to the target phase.
	RegisterHook(target Phase, hook PhaseHook)

	// ── Plan ─────────────────────────────────────────────────────

	// Propose creates a plan and marks it pending approval.
	// Call during Alignment phase for complex tasks (3+ executables).
	Propose(ctx context.Context, p Plan) (*Plan, error)

	// Approve marks the active plan as approved.
	// autoApprove=true means the agent proceeds without per-step confirmations.
	Approve(ctx context.Context, autoApprove bool) error

	// ActivePlan returns the current plan, or nil if none exists.
	ActivePlan() *Plan

	// UpdateExecutable updates the status of one executable in the active plan.
	UpdateExecutable(ctx context.Context, execID string, status ExecutableStatus, result string) error

	// ── Todos ────────────────────────────────────────────────────

	// Todos returns the current checklist.
	Todos() []Todo

	// SetTodos replaces the checklist. Only one todo should be in_progress
	// at a time. Mark completed immediately when done — this drives the UI
	// progress indicator.
	SetTodos(todos []Todo)

	// MarkDone marks a single todo as completed by ID.
	MarkDone(id string)
}

// InMemoryExecutionContext is a simple, non-persistent ExecutionContext.
// Suitable for single-process use and tests. For production, implement
// ExecutionContext against your persistence layer.
type InMemoryExecutionContext struct {
	phase      Phase
	attempt    int
	hooks      map[Phase][]PhaseHook
	activePlan *Plan
	todos      []Todo
}

// NewExecutionContext returns an in-memory ExecutionContext starting at Orientation.
func NewExecutionContext() *InMemoryExecutionContext {
	return &InMemoryExecutionContext{
		phase:   PhaseOrientation,
		attempt: 1,
		hooks:   make(map[Phase][]PhaseHook),
	}
}

func (e *InMemoryExecutionContext) Phase() Phase   { return e.phase }
func (e *InMemoryExecutionContext) Attempt() int   { return e.attempt }
func (e *InMemoryExecutionContext) ActivePlan() *Plan { return e.activePlan }
func (e *InMemoryExecutionContext) Todos() []Todo  { return e.todos }

func (e *InMemoryExecutionContext) Advance(ctx context.Context) error {
	next := e.phase + 1
	if next > PhaseClosure {
		return nil // already at end
	}
	return e.SetPhase(ctx, next)
}

func (e *InMemoryExecutionContext) SetPhase(ctx context.Context, p Phase) error {
	prev := e.phase
	if p == e.phase {
		e.attempt++
	} else {
		e.attempt = 1
	}
	e.phase = p
	for _, hook := range e.hooks[p] {
		if err := hook(ctx, prev, p); err != nil {
			e.phase = prev // rollback
			return err
		}
	}
	return nil
}

func (e *InMemoryExecutionContext) RegisterHook(target Phase, hook PhaseHook) {
	e.hooks[target] = append(e.hooks[target], hook)
}

func (e *InMemoryExecutionContext) Propose(_ context.Context, p Plan) (*Plan, error) {
	p.Approved = false
	e.activePlan = &p
	return &p, nil
}

func (e *InMemoryExecutionContext) Approve(_ context.Context, autoApprove bool) error {
	if e.activePlan == nil {
		return nil
	}
	e.activePlan.Approved = true
	e.activePlan.AutoApprove = autoApprove
	return nil
}

func (e *InMemoryExecutionContext) UpdateExecutable(_ context.Context, execID string, status ExecutableStatus, result string) error {
	if e.activePlan == nil {
		return nil
	}
	for i := range e.activePlan.Executables {
		if e.activePlan.Executables[i].ID == execID {
			if err := ValidateTransition(e.activePlan.Executables[i].Status, status); err != nil {
				return err
			}
			e.activePlan.Executables[i].Status = status
			e.activePlan.Executables[i].Result = result
			return nil
		}
	}
	return nil
}

func (e *InMemoryExecutionContext) SetTodos(todos []Todo) { e.todos = todos }

func (e *InMemoryExecutionContext) MarkDone(id string) {
	for i := range e.todos {
		if e.todos[i].ID == id {
			e.todos[i].Status = TodoStatusCompleted
			return
		}
	}
}
