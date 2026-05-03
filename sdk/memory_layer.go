package autobuild

import (
	"context"
	"sort"
)

// MemoryLayer identifies the origin and priority of a memory entry.
// When two entries conflict, higher priority wins.
//
//	Explicit   > Inferred > Session
//
// This mirrors how Claude handles its own memory:
// explicit user instructions always override inferred context.
type MemoryLayer string

const (
	// MemoryLayerExplicit holds facts the user stated directly.
	// Highest priority. Persists across sessions.
	// Example: "I work at Maxwell Clinic", "Prefer TypeScript over JavaScript"
	MemoryLayerExplicit MemoryLayer = "explicit"

	// MemoryLayerInferred holds facts derived from conversation history.
	// Medium priority. May be overridden by Explicit.
	// Example: "User seems to prefer short responses", "Uses SurrealDB"
	MemoryLayerInferred MemoryLayer = "inferred"

	// MemoryLayerSession holds facts relevant only to this conversation.
	// Lowest priority. Not persisted. Cleared on session end.
	// Example: "User is currently debugging auth flow", "In a hurry today"
	MemoryLayerSession MemoryLayer = "session"
)

// layerPriority maps each layer to a numeric priority (higher = wins).
var layerPriority = map[MemoryLayer]int{
	MemoryLayerExplicit:  3,
	MemoryLayerInferred:  2,
	MemoryLayerSession:   1,
}

// LayeredMemoryEntry is a memory entry with layer metadata.
type LayeredMemoryEntry struct {
	MemoryEntry
	Layer    MemoryLayer `json:"layer"`
	Priority int         `json:"priority"`
}

// LayeredMemoryProvider extends MemoryProvider with layer-aware operations.
// It answers: "given a conflict between two facts, which one wins?"
type LayeredMemoryProvider interface {
	MemoryProvider

	// WriteLayered writes a memory entry at the given layer.
	WriteLayered(ctx context.Context, scope Scope, path string, content string, layer MemoryLayer) error

	// ReadLayered returns a memory entry with its layer metadata.
	ReadLayered(ctx context.Context, scope Scope, path string) (*LayeredMemoryEntry, error)

	// SearchLayered returns entries ranked by layer priority then relevance.
	SearchLayered(ctx context.Context, scope Scope, query string) ([]LayeredMemoryEntry, error)

	// ClearSession removes all session-layer entries.
	// Call at conversation end to avoid stale session context leaking.
	ClearSession(ctx context.Context) error
}

// SortByPriority sorts layered entries so Explicit comes first, Session last.
func SortByPriority(entries []LayeredMemoryEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		pi := layerPriority[entries[i].Layer]
		pj := layerPriority[entries[j].Layer]
		return pi > pj
	})
}

// ResolveConflict returns the winning entry when two entries exist for the
// same fact. Explicit beats Inferred beats Session. If same layer, newer wins
// (caller is responsible for ordering by time before calling).
func ResolveConflict(a, b LayeredMemoryEntry) LayeredMemoryEntry {
	pa := layerPriority[a.Layer]
	pb := layerPriority[b.Layer]
	if pa >= pb {
		return a
	}
	return b
}
