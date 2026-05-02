package autobuild

import (
	"context"
	"encoding/json"
)

// ──────────────────────────────────────────────
// Gate types
// ──────────────────────────────────────────────

// GateType determines how a gate is resolved.
type GateType string

const (
	GateTypeApproval      GateType = "approval"
	GateTypeTimeout       GateType = "timeout"
	GateTypeAutoCondition GateType = "auto_condition"
)

// OnRejectAction determines what happens when a gate is rejected.
type OnRejectAction string

const (
	OnRejectAbort      OnRejectAction = "abort"
	OnRejectRetry      OnRejectAction = "retry"
	OnRejectRouteToStep OnRejectAction = "route_to_step"
)

// Gate blocks step execution until resolved. Three flavours:
//
//   - approval: requires explicit human approval from listed approvers.
//   - timeout: auto-approves after TimeoutMinutes if nobody acts.
//   - auto_condition: evaluates the step's Condition field automatically.
type Gate struct {
	Type               GateType       `json:"type"`
	Approvers          []string       `json:"approvers,omitempty"`
	OnReject           OnRejectAction `json:"on_reject"`
	TimeoutMinutes     int            `json:"timeout_minutes,omitempty"`
	RejectTargetStepID string         `json:"reject_target_step_id,omitempty"`
}

// ──────────────────────────────────────────────
// Condition (branching)
// ──────────────────────────────────────────────

// Operator is a comparison operator for condition evaluation.
type Operator string

const (
	OpEquals      Operator = "equals"
	OpNotEquals   Operator = "not_equals"
	OpContains    Operator = "contains"
	OpNotContains Operator = "not_contains"
	OpGreaterThan Operator = "greater_than"
	OpLessThan    Operator = "less_than"
	OpExists      Operator = "exists"
	OpNotExists   Operator = "not_exists"
)

// Condition enables branching by evaluating a field in the step's output.
// Field supports dot-notation for nested objects (e.g. "metrics.errorRate").
type Condition struct {
	Field    string   `json:"field"`
	Operator Operator `json:"operator"`
	Value    string   `json:"value,omitempty"`
	IfTrue   string   `json:"if_true"`
	IfFalse  string   `json:"if_false"`
}

// ──────────────────────────────────────────────
// Trigger (schedule / webhook)
// ──────────────────────────────────────────────

// TriggerType distinguishes schedule-based from event-based triggers.
type TriggerType string

const (
	TriggerTypeSchedule TriggerType = "schedule"
	TriggerTypeWebhook  TriggerType = "webhook"
)

// Trigger configures automatic execution of a task.
type Trigger struct {
	Type           TriggerType `json:"type"`
	Enabled        bool        `json:"enabled"`
	RRule          string      `json:"rrule,omitempty"`           // RFC 5545, for schedule
	Timezone       string      `json:"timezone,omitempty"`        // IANA tz, for schedule
	SubscriptionID string      `json:"subscription_id,omitempty"` // for webhook
}

// ──────────────────────────────────────────────
// Step
// ──────────────────────────────────────────────

// Step is a single unit of work inside a Task. Steps execute in Position
// order unless NextStepID overrides the flow.
type Step struct {
	ID           string           `json:"id"`
	Content      string           `json:"content"`
	Position     int              `json:"position"`
	NextStepID   *string          `json:"next_step_id,omitempty"` // nil = follow position; pointer so null is expressible
	OutputSchema json.RawMessage  `json:"output_schema,omitempty"`
	Gate         *Gate            `json:"gate,omitempty"`
	Condition    *Condition       `json:"condition,omitempty"`
}

// ──────────────────────────────────────────────
// Task
// ──────────────────────────────────────────────

// Task is a reusable, structured workflow: an ordered sequence of Steps
// with optional gates, conditions, and triggers.
type Task struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Steps          []Step   `json:"steps"`
	Trigger        *Trigger `json:"trigger,omitempty"`
	ParentThreadID string   `json:"parent_thread_id,omitempty"`
	Objective      string   `json:"objective,omitempty"`
}

// StepByID returns the step with the given ID, or nil.
func (t *Task) StepByID(id string) *Step {
	for i := range t.Steps {
		if t.Steps[i].ID == id {
			return &t.Steps[i]
		}
	}
	return nil
}

// FirstStep returns the step with Position 0, or nil if empty.
func (t *Task) FirstStep() *Step {
	for i := range t.Steps {
		if t.Steps[i].Position == 0 {
			return &t.Steps[i]
		}
	}
	if len(t.Steps) > 0 {
		return &t.Steps[0]
	}
	return nil
}

// ──────────────────────────────────────────────
// TaskProvider
// ──────────────────────────────────────────────

// TaskProvider abstracts CRUD and execution of tasks.
type TaskProvider interface {
	// Create persists a new task and returns it with a generated ID.
	Create(ctx context.Context, t Task) (*Task, error)

	// List returns all tasks in the current project.
	List(ctx context.Context) ([]*Task, error)

	// Get returns a single task by ID.
	Get(ctx context.Context, taskID string) (*Task, error)

	// Update modifies an existing task.
	Update(ctx context.Context, t Task) (*Task, error)

	// Delete removes a task.
	Delete(ctx context.Context, taskID string) error

	// Run starts execution of a task, optionally from a specific step.
	// An empty stepID means start from the beginning.
	Run(ctx context.Context, taskID string, stepID string) error
}
