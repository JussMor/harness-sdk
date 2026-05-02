package autobuild

import "context"

// Scope identifies the persistence boundary of a memory entry.
type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

// MemoryProvider abstracts persistent key/value storage that survives across
// conversations and threads. Two scopes exist:
//
//   - User scope: cross-project, holds preferences and profile data.
//   - Project scope: per-project, holds schemas, decisions, workflow state.
type MemoryProvider interface {
	// View returns the content at path. If path is a directory, returns a
	// listing. scope="*" returns both scopes (read-only).
	View(ctx context.Context, scope Scope, path string) (string, error)

	// Create writes a new file at path with the given content.
	// Fails if the file already exists.
	Create(ctx context.Context, scope Scope, path string, content string) error

	// StrReplace performs an exact string replacement inside the file at path.
	// oldStr must appear exactly once.
	StrReplace(ctx context.Context, scope Scope, path string, oldStr, newStr string) error

	// Insert adds text at the given 0-based line number.
	Insert(ctx context.Context, scope Scope, path string, line int, text string) error

	// Delete removes the file or directory at path.
	Delete(ctx context.Context, scope Scope, path string) error

	// Rename moves a file or directory from oldPath to newPath within the same scope.
	Rename(ctx context.Context, scope Scope, oldPath, newPath string) error

	// List returns all file paths under the given directory path.
	List(ctx context.Context, scope Scope, path string) ([]string, error)

	// Search finds memory entries matching a query string.
	Search(ctx context.Context, scope Scope, query string) ([]MemoryEntry, error)
}

// MemoryEntry represents a single memory file with its metadata.
type MemoryEntry struct {
	Path    string `json:"path"`
	Scope   Scope  `json:"scope"`
	Content string `json:"content,omitempty"`
}
