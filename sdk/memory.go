package autobuild

import "context"

// Scope identifies the persistence boundary of a memory entry.
//
// The three scopes mirror Claude's memory model:
//   - User    → cross-project, holds preferences and profile data
//   - Project → per-project, holds schemas, decisions, workflow state
//   - Session → ephemeral, tied to current conversation (use ObservationStore)
type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
	ScopeSession Scope = "session" // delegated to ObservationStore
)

// MemoryProvider abstracts persistent key/value storage that survives across
// conversations and threads.
//
// Note: Insert by line number was removed — use StrReplace instead.
// Line numbers go stale the moment the file changes, which makes them
// dangerous for an agent doing concurrent edits.
type MemoryProvider interface {
	// View returns the content at path. If path is a directory, returns a
	// listing. scope=\"*\" returns both scopes (read-only).
	View(ctx context.Context, scope Scope, path string) (string, error)

	// Create writes a new file at path with the given content.
	// Fails if the file already exists.
	Create(ctx context.Context, scope Scope, path string, content string) error

	// StrReplace performs an exact string replacement inside the file at path.
	// oldStr must appear exactly once.
	StrReplace(ctx context.Context, scope Scope, path string, oldStr, newStr string) error

	// Delete removes the file or directory at path.
	Delete(ctx context.Context, scope Scope, path string) error

	// Rename moves a file or directory from oldPath to newPath within the same scope.
	Rename(ctx context.Context, scope Scope, oldPath, newPath string) error

	// List returns all file paths under the given directory path.
	List(ctx context.Context, scope Scope, path string) ([]string, error)

	// Search finds memory entries matching a query string.
	Search(ctx context.Context, scope Scope, query string) ([]MemoryEntry, error)
}

// MemoryUpdateIntent describes what a memory trigger wants to do.
// Beyond simple Create, the trigger can request Replace or Delete —
// this is how "I no longer work at X" becomes a delete, not a new fact.
type MemoryUpdateIntent struct {
	Layer      MemoryLayer // Explicit/Inferred/Session
	Content    string      // new fact content
	Op         string      // "create", "replace", "delete"
	OldContent string      // for "replace" ops — used with StrReplace
	Path       string      // for "delete" and "replace"; runtime assigns for "create"
	Scope      Scope       // user or project; defaults to ScopeUser
}

// MemoryRoot is one directory read during orientation.
// Having multiple labeled roots mirrors how Claude separates
// user preferences, facts, and project context.
type MemoryRoot struct {
	Scope Scope
	Path  string
	Label string // injected as a header before the content in LayerMemory
}

// DefaultMemoryRoots mirrors Claude's memory structure:
// user profile/preferences, user facts, and project context read separately.
var DefaultMemoryRoots = []MemoryRoot{
	{Scope: ScopeUser, Path: "/profile", Label: "User profile & preferences"},
	{Scope: ScopeUser, Path: "/facts", Label: "Remembered facts"},
	{Scope: ScopeProject, Path: "/", Label: "Project context"},
}

// MemoryEntry represents a single memory file with its metadata.
type MemoryEntry struct {
	Path       string      `json:"path"`
	Scope      Scope       `json:"scope"`
	Content    string      `json:"content,omitempty"`
	Layer      MemoryLayer `json:"layer,omitempty"`      // Explicit/Inferred/Session
	Confidence float64     `json:"confidence,omitempty"` // 0-1, 0 = unknown
	Source     string      `json:"source,omitempty"`     // "user", "inferred", "tool"
	UpdatedAt  int64       `json:"updated_at,omitempty"` // Unix nano
}

// MemorySearchResult is a ranked result from a memory search.
type MemorySearchResult struct {
	MemoryEntry
	Score float64 `json:"score"` // relevance 0-1
}
