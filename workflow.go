package autobuild

import "context"

// Phase represents a stage in the 6-phase workflow lifecycle.
type Phase int

const (
	PhaseOrientation  Phase = iota // Read memory, scan artifacts, load skills
	PhaseAlignment                 // Evaluate clarity, ask ONE question if ambiguous, propose plan
	PhasePreparation               // Checkpoint, create todos, research if knowledge may be stale
	PhaseExecution                 // Do the work — tools in parallel when independent
	PhaseVerification              // Sanity queries, re-read outputs, run tests
	PhaseClosure                   // Final checkpoint, update todos, update memory, suggest next step
)

// String returns the human-readable name of the phase.
func (p Phase) String() string {
	switch p {
	case PhaseOrientation:
		return "orientation"
	case PhaseAlignment:
		return "alignment"
	case PhasePreparation:
		return "preparation"
	case PhaseExecution:
		return "execution"
	case PhaseVerification:
		return "verification"
	case PhaseClosure:
		return "closure"
	default:
		return "unknown"
	}
}

// TodoStatus is the state of a single todo item.
type TodoStatus string

const (
	TodoStatusPending    TodoStatus = "pending"
	TodoStatusInProgress TodoStatus = "in_progress"
	TodoStatusCompleted  TodoStatus = "completed"
)

// Todo is a trackable unit of work within a workflow. Only one todo
// should be in_progress at a time; completed immediately upon finishing.
type Todo struct {
	ID      string     `json:"id"`
	Content string     `json:"content"`
	Status  TodoStatus `json:"status"`
}

// PhaseHook is a callback invoked when the workflow transitions into a phase.
// Returning an error aborts the transition.
type PhaseHook func(ctx context.Context, from, to Phase) error

// WorkflowEngine tracks the current phase of a conversation's lifecycle
// and lets consumers register hooks for phase transitions.
type WorkflowEngine interface {
	// CurrentPhase returns the active phase.
	CurrentPhase() Phase

	// Advance moves to the next phase. Hooks registered for the target phase
	// are invoked before the transition completes. Returns an error if a hook
	// rejects the transition.
	Advance(ctx context.Context) error

	// SetPhase jumps to a specific phase (e.g. for retries). Same hook rules apply.
	SetPhase(ctx context.Context, p Phase) error

	// RegisterHook adds a callback for the given target phase.
	RegisterHook(target Phase, hook PhaseHook)

	// Todos returns the current todo list.
	Todos() []Todo

	// SetTodos replaces the todo list (used when a Task overrides todos).
	SetTodos(todos []Todo)
}
