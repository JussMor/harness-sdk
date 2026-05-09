package autobuild

import "sync"

// TodoStatus is the state of a single todo item.
type TodoStatus string

const (
	TodoStatusPending    TodoStatus = "pending"
	TodoStatusInProgress TodoStatus = "in_progress"
	TodoStatusCompleted  TodoStatus = "completed"
)

// Todo is a trackable unit of work, mirroring Claude Code's TodoWrite tool.
// Only one should be in_progress at a time.
type Todo struct {
	ID         string     `json:"id"`
	Content    string     `json:"content"`
	ActiveForm string     `json:"active_form,omitempty"` // present-continuous label shown while in progress
	Status     TodoStatus `json:"status"`
}

// ExecutionContext tracks the live checklist for a conversation turn.
// It maps directly to Claude Code's todo list behaviour: set todos at the start
// of a complex task, mark each one completed as soon as it is done.
type ExecutionContext interface {
	// Todos returns the current checklist.
	Todos() []Todo

	// SetTodos replaces the checklist. Only one todo should be in_progress
	// at a time. Mark completed immediately when done.
	SetTodos(todos []Todo)

	// MarkDone marks a single todo as completed by ID.
	MarkDone(id string)
}

// InMemoryExecutionContext is a thread-safe, non-persistent ExecutionContext.
type InMemoryExecutionContext struct {
	mu    sync.Mutex
	todos []Todo
}

// NewExecutionContext returns an empty in-memory ExecutionContext.
func NewExecutionContext() *InMemoryExecutionContext {
	return &InMemoryExecutionContext{}
}

func (e *InMemoryExecutionContext) Todos() []Todo {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Todo, len(e.todos))
	copy(out, e.todos)
	return out
}

func (e *InMemoryExecutionContext) SetTodos(todos []Todo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.todos = todos
}

func (e *InMemoryExecutionContext) MarkDone(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.todos {
		if e.todos[i].ID == id {
			e.todos[i].Status = TodoStatusCompleted
			return
		}
	}
}
