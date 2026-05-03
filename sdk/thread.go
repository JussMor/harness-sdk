package autobuild

import "context"

// ThreadStatus represents the lifecycle state of a thread.
//
// A Thread is the persistence unit that links Conversations to projects.
// One Thread can contain multiple Conversations (e.g. a long-running
// project assistant where the user starts a new conversation each day).
//
// Use Thread for: project-scoped persistence, message routing, archival.
// Use Conversation for: in-memory turn state, message history.
// Use Subagent for: parallel forked execution.
type ThreadStatus string

const (
	ThreadStatusActive    ThreadStatus = "active"
	ThreadStatusCompleted ThreadStatus = "completed"
	ThreadStatusFailed    ThreadStatus = "failed"
	ThreadStatusArchived  ThreadStatus = "archived"
)

// Thread is metadata for a long-running execution context.
// It exists separately from Conversation to allow many conversations to
// share project scope, mode, and persistence.
type Thread struct {
	ID        string       `json:"id"`
	ProjectID string       `json:"project_id,omitempty"`
	ModeID    string       `json:"mode_id,omitempty"`
	Status    ThreadStatus `json:"status"`
	ParentID  string       `json:"parent_id,omitempty"` // for hierarchical threads
}

// ThreadProvider abstracts thread lifecycle and message routing.
// Implement this when you need persistent, addressable conversation hosts
// (e.g. multi-user, multi-project apps). Skip if you only need single-user
// in-memory conversations — Conversation alone is enough.
type ThreadProvider interface {
	// Create starts a new thread.
	Create(ctx context.Context, projectID, modeID string) (*Thread, error)

	// Get returns thread metadata by ID.
	Get(ctx context.Context, threadID string) (*Thread, error)

	// Archive marks a thread as archived.
	Archive(ctx context.Context, threadID string) error

	// SendMessage delivers a message to another thread (cross-thread comms).
	SendMessage(ctx context.Context, msg Message) error
}
