package autobuild

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ─── Memory recall (header-only scan + LLM selector) ──────────────────────
//
// Mirrors Claude Code's memdir/findRelevantMemories: scan all memory files,
// read only their frontmatter (cheap), and ask a small/fast LLM to pick the
// few that are actually relevant to the user's query. This avoids loading
// all memory content into every turn.

// MemoryHeader is the metadata portion of a memory file used during recall.
type MemoryHeader struct {
	Path        string     `json:"path"`
	Scope       Scope      `json:"scope"`
	Type        MemoryType `json:"type,omitempty"`
	Description string     `json:"description,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at,omitempty"`
}

// MemoryRecaller selects memory files relevant to a user query.
//
// Implementations should respect:
//   - AlreadySurfaced: paths shown in earlier turns of this conversation
//     (don't re-pick them; let the recall budget go to fresh candidates).
//   - RecentTools: tools the agent has called this session — usage docs
//     for those should NOT be surfaced (the conversation already shows
//     working invocations). Warnings/gotchas about those tools STILL
//     should surface; the implementation distinguishes via description.
type MemoryRecaller interface {
	Recall(ctx context.Context, opts RecallOptions) ([]MemoryHeader, error)
}

// RecallOptions are the inputs to MemoryRecaller.Recall.
type RecallOptions struct {
	Query           string
	Headers         []MemoryHeader
	MaxResults      int
	RecentTools     []string
	AlreadySurfaced map[string]bool
}

// LLMMemoryRecaller asks a small, fast LLM to select the most relevant
// memory files based on filename, description, type, and freshness.
//
// The selector sees the memory MANIFEST (one line per file) — never the
// full content. This is cheap: a single short LLM call returning up to
// MaxResults filenames.
type LLMMemoryRecaller struct {
	Provider LLMProvider
	Model    string
}

const llmMemoryRecallSystemPrompt = `You are selecting memories that will help an AI agent process a user's query. You are given the query and a list of memory files with their filenames, types, descriptions, and ages.

Return up to N filenames for memories that will CLEARLY help. Be selective:
- If unsure whether a memory will help, do not include it.
- If no memory clearly applies, return an empty list.
- If "Recently used tools" is provided, do NOT select usage reference / API docs for those tools (the agent is already using them). DO still select memories containing warnings, gotchas, or known issues about those tools — active use is exactly when those matter.

Output JSON only: {"selected":["filename1.md","filename2.md"]}`

// Recall implements MemoryRecaller.
func (r *LLMMemoryRecaller) Recall(ctx context.Context, opts RecallOptions) ([]MemoryHeader, error) {
	if r.Provider == nil || len(opts.Headers) == 0 {
		return nil, nil
	}
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = 5
	}

	// Filter already-surfaced before sending to the selector so it doesn't
	// burn its budget re-picking files the caller will discard.
	candidates := make([]MemoryHeader, 0, len(opts.Headers))
	for _, h := range opts.Headers {
		if opts.AlreadySurfaced[h.Path] {
			continue
		}
		candidates = append(candidates, h)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	manifest := FormatMemoryManifest(candidates)
	toolsLine := ""
	if len(opts.RecentTools) > 0 {
		toolsLine = "\nRecently used tools: " + strings.Join(opts.RecentTools, ", ")
	}

	prompt := fmt.Sprintf(
		"%s\n\nQuery: %s\n\nReturn at most %d filenames.\n\nAvailable memories:\n%s%s",
		llmMemoryRecallSystemPrompt, opts.Query, maxResults, manifest, toolsLine,
	)

	resp, err := r.Provider.Chat(ctx, ChatRequest{
		Model: r.Model,
		Messages: []ChatMessage{
			{Role: RoleUser, Content: prompt},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("memory recall: %w", err)
	}

	selected := parseSelectedFilenames(resp.Content)
	if len(selected) == 0 {
		return nil, nil
	}

	byPath := make(map[string]MemoryHeader, len(candidates))
	byBase := make(map[string]MemoryHeader, len(candidates))
	for _, h := range candidates {
		byPath[h.Path] = h
		byBase[basenameOf(h.Path)] = h
	}

	out := make([]MemoryHeader, 0, len(selected))
	for _, f := range selected {
		if h, ok := byPath[f]; ok {
			out = append(out, h)
			continue
		}
		if h, ok := byBase[f]; ok {
			out = append(out, h)
		}
		if len(out) >= maxResults {
			break
		}
	}
	return out, nil
}

// FormatMemoryManifest renders headers as one-line entries:
//
//	- [type] path (age): description
//
// Used by both the recall selector and any UI that needs a compact view.
func FormatMemoryManifest(headers []MemoryHeader) string {
	// Newest first — matches what the selector LLM expects to see.
	sorted := make([]MemoryHeader, len(headers))
	copy(sorted, headers)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].UpdatedAt.After(sorted[j].UpdatedAt)
	})

	var b strings.Builder
	for _, h := range sorted {
		tag := ""
		if h.Type != "" {
			tag = "[" + string(h.Type) + "] "
		}
		age := ""
		if !h.UpdatedAt.IsZero() {
			age = " (" + MemoryAge(h.UpdatedAt) + ")"
		}
		b.WriteString("- ")
		b.WriteString(tag)
		b.WriteString(h.Path)
		b.WriteString(age)
		if strings.TrimSpace(h.Description) != "" {
			b.WriteString(": ")
			b.WriteString(strings.TrimSpace(h.Description))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// parseSelectedFilenames extracts a JSON object {"selected":[...]} from
// the model's output. Tolerates markdown fencing and surrounding prose.
func parseSelectedFilenames(content string) []string {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return nil
	}
	raw := content[start : end+1]

	// Cheap manual extraction — avoid pulling in extra imports for a
	// simple JSON shape. We look for the array body after "selected".
	idx := strings.Index(raw, "\"selected\"")
	if idx < 0 {
		return nil
	}
	open := strings.Index(raw[idx:], "[")
	close := strings.Index(raw[idx:], "]")
	if open < 0 || close <= open {
		return nil
	}
	arr := raw[idx+open+1 : idx+close]

	var out []string
	for _, part := range strings.Split(arr, ",") {
		s := strings.TrimSpace(part)
		s = strings.Trim(s, "\"' \t\n\r")
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func basenameOf(path string) string {
	if i := strings.LastIndexAny(path, "/\\"); i >= 0 {
		return path[i+1:]
	}
	return path
}
