package autobuild

import "context"

// RunnerTier determines the compute/reasoning level of a spawned runner.
type RunnerTier string

const (
	// RunnerTierNano is for simple, high-volume tasks with minimal cost.
	RunnerTierNano RunnerTier = "nano"

	// RunnerTierMini is for tasks requiring reasoning, code, or judgment.
	RunnerTierMini RunnerTier = "mini"
)

// ObjectiveStatus is the status reported by a child thread to its parent.
type ObjectiveStatus string

const (
	ObjectiveStatusSuccess       ObjectiveStatus = "success"
	ObjectiveStatusFailure       ObjectiveStatus = "failure"
	ObjectiveStatusInputRequired ObjectiveStatus = "input-required"
	ObjectiveStatusPending       ObjectiveStatus = "pending"
)

// ThreadStatus represents the lifecycle state of a thread.
type ThreadStatus string

const (
	ThreadStatusActive    ThreadStatus = "active"
	ThreadStatusCompleted ThreadStatus = "completed"
	ThreadStatusFailed    ThreadStatus = "failed"
	ThreadStatusArchived  ThreadStatus = "archived"
)

// Thread is the unit of execution — a conversation with history, todos,
// and execution context. Threads share the project workspace but have
// independent sandbox state.
type Thread struct {
	ID              string       `json:"id"`
	ProjectID       string       `json:"project_id"`
	ModeID          string       `json:"mode_id"`
	Status          ThreadStatus `json:"status"`
	ParentThreadID  string       `json:"parent_thread_id,omitempty"`
	IndependentShell bool        `json:"independent_shell,omitempty"`
}

// Runner is a fire-and-forget subthread spawned by an orchestrator.
// It receives a self-contained task description and a resource bundle.
type Runner struct {
	ID             string           `json:"id"`
	Tier           RunnerTier       `json:"tier"`
	Task           string           `json:"task"`
	ResourceBundle []ResourceRef    `json:"resource_bundle,omitempty"`
	ThreadID       string           `json:"thread_id,omitempty"`
	Status         ObjectiveStatus  `json:"status,omitempty"`
	Result         string           `json:"result,omitempty"`
}

// ResourceRef is a reference to an artifact or sheet passed to a runner
// as part of its resource bundle.
type ResourceRef struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// ObjectiveReport is the payload sent by a child thread back to its parent
// via ReportStatus.
type ObjectiveReport struct {
	Status  ObjectiveStatus `json:"status"`
	Summary string          `json:"summary"`
	Result  map[string]any  `json:"result,omitempty"`
}

// ThreadProvider abstracts thread lifecycle, runner spawning, and
// inter-thread communication.
type ThreadProvider interface {
	// Spawn creates a new runner subthread and returns its ID.
	Spawn(ctx context.Context, r Runner) (threadID string, err error)

	// Archive marks a thread as archived.
	Archive(ctx context.Context, threadID string) error

	// SendMessage delivers a message to another thread.
	SendMessage(ctx context.Context, msg Message) error

	// ReportStatus sends an objective report from a child thread to its parent.
	ReportStatus(ctx context.Context, parentThreadID string, report ObjectiveReport) error

	// Get returns the thread metadata.
	Get(ctx context.Context, threadID string) (*Thread, error)
}
