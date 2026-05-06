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
	UserID    string       `json:"user_id,omitempty"`    // tenant isolation; empty = single-user
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
	// Create starts a new thread with no user scoping.
	Create(ctx context.Context, projectID, modeID string) (*Thread, error)

	// Get returns thread metadata by ID.
	Get(ctx context.Context, threadID string) (*Thread, error)

	// Archive marks a thread as archived.
	Archive(ctx context.Context, threadID string) error

	// SendMessage delivers a message to another thread (cross-thread comms).
	SendMessage(ctx context.Context, msg Message) error
}

// MultiUserThreadProvider extends ThreadProvider with per-user scoping.
// Implement this for multi-tenant deployments where threads belong to users.
//
// Implementations must enforce that a user can only Get/List/Archive their
// own threads — return ErrThreadAccessDenied when accessed with the wrong UserID.
type MultiUserThreadProvider interface {
	ThreadProvider

	// CreateForUser starts a thread owned by userID.
	CreateForUser(ctx context.Context, userID, projectID, modeID string) (*Thread, error)

	// GetForUser returns a thread only if it belongs to userID.
	// Returns ErrThreadAccessDenied if the thread exists but belongs to another user.
	GetForUser(ctx context.Context, userID, threadID string) (*Thread, error)

	// ListByUser returns all threads owned by userID, optionally filtered by status.
	ListByUser(ctx context.Context, userID string, status ThreadStatus) ([]*Thread, error)
}

// ErrThreadAccessDenied is returned when a user tries to access a thread
// owned by another user.
var ErrThreadAccessDenied = &threadError{"thread: access denied"}

type threadError struct{ msg string }

func (e *threadError) Error() string { return e.msg }
