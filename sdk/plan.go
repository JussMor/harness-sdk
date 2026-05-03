package autobuild

import (
	"fmt"
)

// ExecutableStatus tracks progress of a single executable in a plan's DAG.
//
// State machine:
//
//	planned ──→ queued ──→ in_progress ──→ in_review ──→ completed
//	                           │                            ↑
//	                           └────────────────────────────┘
//	                           (non-PR work skips in_review)
//
//	From any non-terminal state:
//	  → failed     (unrecoverable error)
//	  → blocked    (external dependency)
//	  → cancelled  (no longer needed)
type ExecutableStatus string

const (
	ExecStatusPlanned    ExecutableStatus = "planned"
	ExecStatusQueued     ExecutableStatus = "queued"
	ExecStatusNotStarted ExecutableStatus = "not_started" // alias for planned (backward compat)
	ExecStatusInProgress ExecutableStatus = "in_progress"
	ExecStatusInReview   ExecutableStatus = "in_review"
	ExecStatusCompleted  ExecutableStatus = "completed"
	ExecStatusFailed     ExecutableStatus = "failed"
	ExecStatusBlocked    ExecutableStatus = "blocked"
	ExecStatusCancelled  ExecutableStatus = "cancelled"
)

// IsTerminal returns true if no further transitions are allowed from this status.
func (s ExecutableStatus) IsTerminal() bool {
	return s == ExecStatusCompleted || s == ExecStatusFailed || s == ExecStatusCancelled
}

// CanTransitionTo returns true if moving from s to target is a valid transition.
func (s ExecutableStatus) CanTransitionTo(target ExecutableStatus) bool {
	targets, ok := validTransitions[s]
	if !ok {
		return false
	}
	return targets[target]
}

// validTransitions defines the allowed (from → to) state transitions.
var validTransitions = map[ExecutableStatus]map[ExecutableStatus]bool{
	ExecStatusPlanned: {
		ExecStatusQueued: true, ExecStatusFailed: true,
		ExecStatusBlocked: true, ExecStatusCancelled: true,
	},
	ExecStatusNotStarted: { // same as planned
		ExecStatusQueued: true, ExecStatusInProgress: true,
		ExecStatusFailed: true, ExecStatusBlocked: true, ExecStatusCancelled: true,
	},
	ExecStatusQueued: {
		ExecStatusInProgress: true, ExecStatusFailed: true,
		ExecStatusBlocked: true, ExecStatusCancelled: true,
	},
	ExecStatusInProgress: {
		ExecStatusInReview: true, ExecStatusCompleted: true,
		ExecStatusFailed: true, ExecStatusBlocked: true, ExecStatusCancelled: true,
	},
	ExecStatusInReview: {
		ExecStatusCompleted: true, ExecStatusFailed: true,
		ExecStatusBlocked: true, ExecStatusCancelled: true,
	},
	ExecStatusBlocked: {
		ExecStatusQueued: true, ExecStatusFailed: true, ExecStatusCancelled: true,
	},
}

// ValidateTransition returns nil if the transition is valid, or an error
// describing why not.
func ValidateTransition(from, to ExecutableStatus) error {
	if from == to {
		return fmt.Errorf("no-op transition: already %s", from)
	}
	if from.IsTerminal() {
		return fmt.Errorf("cannot transition from terminal state %q", from)
	}
	if !from.CanTransitionTo(to) {
		return fmt.Errorf("invalid transition: %s → %s", from, to)
	}
	return nil
}

// Executable is a unit of work in a plan's DAG. Each executable maps to a
// child-thread agent that plans, codes, tests, PRs, and merges.
type Executable struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Description  string           `json:"description,omitempty"`
	Dependencies []string         `json:"dependencies,omitempty"` // IDs of executables that must complete first
	Status       ExecutableStatus `json:"status"`
	ThreadID     string           `json:"thread_id,omitempty"`
	Result       string           `json:"result,omitempty"`
	Retries      int              `json:"retries,omitempty"`
	MaxRetries   int              `json:"max_retries,omitempty"`
}

// Plan is a structured proposal for complex work: a title, objective,
// and a DAG of executables with dependency relationships.
type Plan struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Objective   string       `json:"objective"`
	Executables []Executable `json:"executables"`
	Approved    bool         `json:"approved"`
	AutoApprove bool         `json:"auto_approve,omitempty"`
}

// ──────────────────────────────────────────────
// DAG helpers
// ──────────────────────────────────────────────

// NextReady returns executables whose dependencies are all completed and
// that are still in a pre-dispatch state (planned or not_started).
// These can be dispatched in parallel.
func (p *Plan) NextReady() []Executable {
	completed := make(map[string]bool, len(p.Executables))
	for _, e := range p.Executables {
		if e.Status == ExecStatusCompleted {
			completed[e.ID] = true
		}
	}

	var ready []Executable
	for _, e := range p.Executables {
		if e.Status != ExecStatusNotStarted && e.Status != ExecStatusPlanned {
			continue
		}
		allDeps := true
		for _, dep := range e.Dependencies {
			if !completed[dep] {
				allDeps = false
				break
			}
		}
		if allDeps {
			ready = append(ready, e)
		}
	}
	return ready
}

// IsBlocked returns true if the executable cannot proceed because at least
// one of its dependencies has not completed.
func (p *Plan) IsBlocked(execID string) bool {
	var target *Executable
	for i := range p.Executables {
		if p.Executables[i].ID == execID {
			target = &p.Executables[i]
			break
		}
	}
	if target == nil {
		return true
	}

	completed := make(map[string]bool, len(p.Executables))
	for _, e := range p.Executables {
		if e.Status == ExecStatusCompleted {
			completed[e.ID] = true
		}
	}
	for _, dep := range target.Dependencies {
		if !completed[dep] {
			return true
		}
	}
	return false
}

// IsComplete returns true if all executables are in a terminal state
// (completed, failed, or cancelled).
func (p *Plan) IsComplete() bool {
	for _, e := range p.Executables {
		if !e.Status.IsTerminal() {
			return false
		}
	}
	return len(p.Executables) > 0
}

// ExecutableByID returns the executable with the given ID, or nil.
func (p *Plan) ExecutableByID(id string) *Executable {
	for i := range p.Executables {
		if p.Executables[i].ID == id {
			return &p.Executables[i]
		}
	}
	return nil
}

// PlanProvider has been removed. Plan lifecycle (propose, approve, update)
// is now owned by ExecutionContext, which keeps phase, plan, and todos
// in a single coherent object. See execution_context.go.
