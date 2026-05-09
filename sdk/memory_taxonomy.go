package autobuild

// MemoryType is the closed taxonomy that constrains what gets persisted.
// Mirrors Claude Code's memdir model: only context that is NOT derivable
// from the current project state is worth saving as a memory.
//
// Code patterns, architecture, git history, and file structure are
// derivable (via grep/git/CLAUDE.md) and should NOT be saved as memories.
type MemoryType string

const (
	// MemoryTypeUser captures the user's role, preferences, expertise,
	// and responsibilities. Used to tailor explanations and suggestions
	// (e.g. frame frontend explanations in terms of backend analogues
	// for a Go-deep, React-new user).
	MemoryTypeUser MemoryType = "user"

	// MemoryTypeFeedback records corrections AND confirmations the user
	// gave about how to approach work. Save with **Why:** and **How to
	// apply:** so future-you can judge edge cases instead of blindly
	// following the rule.
	MemoryTypeFeedback MemoryType = "feedback"

	// MemoryTypeProject tracks ongoing work, goals, deadlines, incidents
	// — context not derivable from code or git history. These decay fast,
	// so always include the why and convert relative dates to absolute.
	MemoryTypeProject MemoryType = "project"

	// MemoryTypeReference points to external systems (Linear projects,
	// Grafana boards, Slack channels) where up-to-date info lives.
	MemoryTypeReference MemoryType = "reference"
)

// AllMemoryTypes is the canonical ordering used in prompts and validation.
var AllMemoryTypes = []MemoryType{
	MemoryTypeUser,
	MemoryTypeFeedback,
	MemoryTypeProject,
	MemoryTypeReference,
}

// ParseMemoryType returns the parsed type or empty string if invalid.
// Legacy files without a type field degrade gracefully (return "").
func ParseMemoryType(raw string) MemoryType {
	for _, t := range AllMemoryTypes {
		if string(t) == raw {
			return t
		}
	}
	return ""
}

// IsValid reports whether the type is one of the four canonical values.
func (t MemoryType) IsValid() bool { return ParseMemoryType(string(t)) != "" }
