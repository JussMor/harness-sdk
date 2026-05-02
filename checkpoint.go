package autobuild

import (
	"context"
	"time"
)

// Checkpoint is a safety snapshot of the project state taken before or
// after mutations. Enables rollback if something goes wrong.
type Checkpoint struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	ProjectID   string    `json:"project_id,omitempty"`
	ThreadID    string    `json:"thread_id,omitempty"`
}

// CheckpointProvider abstracts creating, listing, and restoring snapshots.
// The autobuild protocol requires checkpoints before AND after any artifact
// mutation — no exceptions.
type CheckpointProvider interface {
	// Create takes a snapshot with the given description and returns its ID.
	Create(ctx context.Context, description string) (*Checkpoint, error)

	// Restore rolls back the project to the state captured by the checkpoint.
	Restore(ctx context.Context, checkpointID string) error

	// List returns all checkpoints for the current project, newest first.
	List(ctx context.Context) ([]*Checkpoint, error)
}
