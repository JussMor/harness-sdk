package autobuild

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════
// memdir — typed-memory taxonomy + helpers (Claude Code parity)
// ═══════════════════════════════════════════════════════════════════════
//
// Mirrors Claude Code's `memdir/` module:
//   - Closed taxonomy of four memory types (user / feedback / project / reference)
//   - frontmatter-driven scanning of memory directories
//   - relative age formatting ("3 days ago") and freshness staleness notes
//   - relevance selection via pluggable selector
//   - read-before-write contract enforcement primitive
//
// Reference: docs/Claude Code/memdir/{memoryTypes.ts, memoryAge.ts,
// memoryScan.ts, findRelevantMemories.ts}.

// ── Taxonomy ─────────────────────────────────────────────────────────────────

// MemoryType is the closed four-type taxonomy for persistent memory entries.
//
// Content NOT derivable from the project (preferences, corrections, ongoing
// initiatives, external pointers) goes into one of these types. Code patterns,
// architecture, and git history are derivable and must NOT be stored as memory.
type MemoryType string

const (
	MemoryTypeUser      MemoryType = "user"      // user role, preferences, knowledge
	MemoryTypeFeedback  MemoryType = "feedback"  // corrections + validated approaches
	MemoryTypeProject   MemoryType = "project"   // ongoing work, deadlines, motivations
	MemoryTypeReference MemoryType = "reference" // pointers to external systems
)

// MemoryTypes lists the canonical taxonomy in declaration order.
var MemoryTypes = []MemoryType{
	MemoryTypeUser,
	MemoryTypeFeedback,
	MemoryTypeProject,
	MemoryTypeReference,
}

// ParseMemoryType returns a valid MemoryType or empty string for unknown input.
// Empty / unknown values degrade gracefully (legacy memories without `type:`
// field keep working).
func ParseMemoryType(raw string) MemoryType {
	v := strings.ToLower(strings.TrimSpace(raw))
	for _, t := range MemoryTypes {
		if string(t) == v {
			return t
		}
	}
	return ""
}

// IsValidMemoryType reports whether t is in the closed taxonomy.
func IsValidMemoryType(t MemoryType) bool { return ParseMemoryType(string(t)) != "" }

// ── Optional MemoryProvider extension: stat ─────────────────────────────────

// MemoryStat is metadata returned by MemoryStater.
type MemoryStat struct {
	MtimeMs int64 // Unix milliseconds of last modification
	Size    int64
	IsDir   bool
}

// MemoryStater is an optional interface a MemoryProvider may implement to
// expose mtime/size. memdir uses it for age annotations and freshness sorting.
// Providers that don't implement it fall back to time.Now() — relative age
// will be "today".
type MemoryStater interface {
	Stat(ctx context.Context, scope Scope, path string) (MemoryStat, error)
}

// ── Memory headers / scan ────────────────────────────────────────────────────

// MemoryHeader is the parsed-frontmatter metadata for a single memory file.
type MemoryHeader struct {
	Scope       Scope      `json:"scope"`
	Path        string     `json:"path"` // path within the scope, e.g. "/feedback/testing.md"
	Filename    string     `json:"filename"`
	Name        string     `json:"name,omitempty"`
	Description string     `json:"description,omitempty"`
	Type        MemoryType `json:"type,omitempty"`
	MtimeMs     int64      `json:"mtime_ms,omitempty"`
}

// MaxMemoryFilesScanned caps the manifest size for relevance selection.
const MaxMemoryFilesScanned = 200

// ScanMemoryFiles walks the scope/dir tree, parses frontmatter from every
// `.md` file, and returns headers sorted newest-first (capped at
// MaxMemoryFilesScanned). Files named MEMORY.md are excluded — they're loaded
// directly into the system prompt.
//
// Errors on individual files are swallowed (best-effort). A directory that
// doesn't exist returns an empty slice, no error.
func ScanMemoryFiles(ctx context.Context, m MemoryProvider, scope Scope, dir string) ([]MemoryHeader, error) {
	if m == nil {
		return nil, nil
	}
	paths, err := m.List(ctx, scope, dir)
	if err != nil {
		return nil, nil // listing failure → empty manifest
	}
	stater, _ := m.(MemoryStater)
	out := make([]MemoryHeader, 0, len(paths))
	for _, p := range paths {
		if !strings.HasSuffix(strings.ToLower(p), ".md") {
			continue
		}
		base := filepath.Base(p)
		if strings.EqualFold(base, "MEMORY.md") {
			continue
		}
		content, err := m.View(ctx, scope, p)
		if err != nil || content == "" {
			continue
		}
		h := MemoryHeader{Scope: scope, Path: p, Filename: base}
		fields, _, _, ferr := parseFrontmatter(content)
		if ferr == nil {
			h.Name = strings.TrimSpace(fields["name"])
			h.Description = strings.TrimSpace(fields["description"])
			h.Type = ParseMemoryType(fields["type"])
		}
		if stater != nil {
			if st, err := stater.Stat(ctx, scope, p); err == nil {
				h.MtimeMs = st.MtimeMs
			}
		}
		if h.MtimeMs == 0 {
			h.MtimeMs = time.Now().UnixMilli()
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MtimeMs > out[j].MtimeMs })
	if len(out) > MaxMemoryFilesScanned {
		out = out[:MaxMemoryFilesScanned]
	}
	return out, nil
}

// FormatMemoryManifest renders headers as one-line entries:
//
//	- [type] /path/file.md (2026-03-10T12:00:00Z): description
func FormatMemoryManifest(headers []MemoryHeader) string {
	if len(headers) == 0 {
		return ""
	}
	var b strings.Builder
	for _, h := range headers {
		b.WriteString("- ")
		if h.Type != "" {
			b.WriteString("[")
			b.WriteString(string(h.Type))
			b.WriteString("] ")
		}
		b.WriteString(h.Path)
		if h.MtimeMs > 0 {
			b.WriteString(" (")
			b.WriteString(time.UnixMilli(h.MtimeMs).UTC().Format(time.RFC3339))
			b.WriteString(")")
		}
		if h.Description != "" {
			b.WriteString(": ")
			b.WriteString(h.Description)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ── Age / freshness ──────────────────────────────────────────────────────────

// MemoryAgeDays is days since mtime, floor-rounded. Negative inputs (clock
// skew / future mtime) clamp to 0.
func MemoryAgeDays(mtimeMs int64) int {
	if mtimeMs <= 0 {
		return 0
	}
	d := (time.Now().UnixMilli() - mtimeMs) / 86_400_000
	if d < 0 {
		return 0
	}
	return int(d)
}

// MemoryAge is the human-readable form: "today" / "yesterday" / "N days ago".
// Models reason about "47 days ago" much better than ISO timestamps.
func MemoryAge(mtimeMs int64) string {
	d := MemoryAgeDays(mtimeMs)
	switch d {
	case 0:
		return "today"
	case 1:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", d)
	}
}

// MemoryFreshnessText returns a plain-text staleness caveat for memories
// older than 1 day. Returns "" for fresh memories.
func MemoryFreshnessText(mtimeMs int64) string {
	d := MemoryAgeDays(mtimeMs)
	if d <= 1 {
		return ""
	}
	return fmt.Sprintf(
		"This memory is %d days old. Memories are point-in-time observations, "+
			"not live state — claims about code behavior or file:line citations "+
			"may be outdated. Verify against current code before asserting as fact.",
		d,
	)
}

// MemoryFreshnessNote wraps MemoryFreshnessText in <system-reminder> tags,
// or returns "" for fresh memories.
func MemoryFreshnessNote(mtimeMs int64) string {
	t := MemoryFreshnessText(mtimeMs)
	if t == "" {
		return ""
	}
	return "<system-reminder>" + t + "</system-reminder>\n"
}

// ── Relevance selection ──────────────────────────────────────────────────────

// MemorySelector picks up to N memory headers most relevant to a query. The
// default is a keyword-overlap selector (KeywordMemorySelector); callers can
// plug in an LLM-backed selector for higher-fidelity recall.
type MemorySelector interface {
	Select(ctx context.Context, query string, headers []MemoryHeader, limit int) ([]MemoryHeader, error)
}

// KeywordMemorySelector ranks headers by keyword overlap between the query
// and each header's name+description+type+path. No LLM call required.
type KeywordMemorySelector struct{}

func (KeywordMemorySelector) Select(_ context.Context, query string, headers []MemoryHeader, limit int) ([]MemoryHeader, error) {
	terms := tokenizeQuery(query)
	if len(terms) == 0 || len(headers) == 0 {
		return nil, nil
	}
	type scored struct {
		h     MemoryHeader
		score int
	}
	out := make([]scored, 0, len(headers))
	for _, h := range headers {
		hay := strings.ToLower(h.Name + " " + h.Description + " " + string(h.Type) + " " + h.Path)
		s := 0
		for _, t := range terms {
			if strings.Contains(hay, t) {
				s++
			}
		}
		if s > 0 {
			out = append(out, scored{h, s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	res := make([]MemoryHeader, len(out))
	for i, s := range out {
		res[i] = s.h
	}
	return res, nil
}

func tokenizeQuery(q string) []string {
	q = strings.ToLower(q)
	var out []string
	for _, w := range strings.FieldsFunc(q, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '.', ',', '!', '?', ';', ':', '"', '\'', '(', ')', '[', ']', '{', '}', '/', '\\':
			return true
		}
		return false
	}) {
		if len(w) > 2 {
			out = append(out, w)
		}
	}
	return out
}

// FindRelevantMemoriesOptions configures a relevance lookup.
type FindRelevantMemoriesOptions struct {
	Scopes          []Scope        // defaults to {ScopeUser, ScopeProject}
	Dir             string         // root dir per scope (default "/")
	Limit           int            // defaults to 5
	Selector        MemorySelector // defaults to KeywordMemorySelector
	AlreadySurfaced map[string]bool // {scope|path} keys to exclude (already shown)
}

// RelevantMemory pairs a header with its (already-loaded) content for direct
// injection into the prompt. Content is best-effort; on read error the
// memory is dropped.
type RelevantMemory struct {
	Header  MemoryHeader
	Content string
}

// FindRelevantMemories scans configured scopes, asks the selector for the
// most relevant headers, then loads the content for each. Returns up to
// opts.Limit results across all scopes.
func FindRelevantMemories(ctx context.Context, m MemoryProvider, query string, opts FindRelevantMemoriesOptions) ([]RelevantMemory, error) {
	if m == nil || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	scopes := opts.Scopes
	if len(scopes) == 0 {
		scopes = []Scope{ScopeUser, ScopeProject}
	}
	dir := opts.Dir
	if dir == "" {
		dir = "/"
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 5
	}
	sel := opts.Selector
	if sel == nil {
		sel = KeywordMemorySelector{}
	}

	all := make([]MemoryHeader, 0, 32)
	for _, sc := range scopes {
		hs, _ := ScanMemoryFiles(ctx, m, sc, dir)
		for _, h := range hs {
			key := string(h.Scope) + "|" + h.Path
			if opts.AlreadySurfaced[key] {
				continue
			}
			all = append(all, h)
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	picked, err := sel.Select(ctx, query, all, limit)
	if err != nil {
		return nil, err
	}
	out := make([]RelevantMemory, 0, len(picked))
	for _, h := range picked {
		content, err := m.View(ctx, h.Scope, h.Path)
		if err != nil || content == "" {
			continue
		}
		out = append(out, RelevantMemory{Header: h, Content: content})
	}
	return out, nil
}

// ── Read-before-write contract ───────────────────────────────────────────────

// ReadBeforeWriteTracker enforces the contract that a memory file must be
// viewed (or freshly created) in the current session before it can be
// updated or deleted. Mirrors Claude Code's "read-before-write" guard for
// FileEditTool, applied to typed memory.
//
// Safe for concurrent use within a session.
type ReadBeforeWriteTracker struct {
	mu   sync.Mutex
	seen map[string]int64 // key=scope|path → mtime observed at view time
}

// NewReadBeforeWriteTracker returns an empty tracker.
func NewReadBeforeWriteTracker() *ReadBeforeWriteTracker {
	return &ReadBeforeWriteTracker{seen: map[string]int64{}}
}

func rbwKey(scope Scope, path string) string { return string(scope) + "|" + path }

// MarkRead records that the caller has viewed the entry. mtimeMs may be 0
// when the provider doesn't support stat — staleness checks then skip.
func (t *ReadBeforeWriteTracker) MarkRead(scope Scope, path string, mtimeMs int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seen[rbwKey(scope, path)] = mtimeMs
}

// HasRead reports whether the caller has viewed the entry in this session.
func (t *ReadBeforeWriteTracker) HasRead(scope Scope, path string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.seen[rbwKey(scope, path)]
	return ok
}

// CheckFreshness returns nil if the seen mtime matches currentMtimeMs (or
// either is 0 = unknown). Otherwise, the file changed under us.
func (t *ReadBeforeWriteTracker) CheckFreshness(scope Scope, path string, currentMtimeMs int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev, ok := t.seen[rbwKey(scope, path)]
	if !ok {
		return fmt.Errorf("memory %s not read in this session — view it before editing", path)
	}
	if prev == 0 || currentMtimeMs == 0 {
		return nil
	}
	if prev != currentMtimeMs {
		return fmt.Errorf("memory %s changed since last read (mtime drift) — view it again before editing", path)
	}
	return nil
}

// Forget drops the tracking entry (call on Delete/Rename to invalidate).
func (t *ReadBeforeWriteTracker) Forget(scope Scope, path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.seen, rbwKey(scope, path))
}

// ── Frontmatter helpers for typed memory writes ──────────────────────────────

// MemoryFrontmatter is the canonical header for a typed-memory file.
type MemoryFrontmatter struct {
	Name        string
	Description string
	Type        MemoryType
}

// Render returns a `---`-delimited YAML block ready to prepend to the body.
// Empty fields are omitted; an empty struct returns "".
func (f MemoryFrontmatter) Render() string {
	if f.Name == "" && f.Description == "" && f.Type == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("---\n")
	if f.Name != "" {
		fmt.Fprintf(&b, "name: %s\n", f.Name)
	}
	if f.Description != "" {
		fmt.Fprintf(&b, "description: %s\n", f.Description)
	}
	if f.Type != "" {
		fmt.Fprintf(&b, "type: %s\n", f.Type)
	}
	b.WriteString("---\n\n")
	return b.String()
}

// ParseMemoryFrontmatter extracts (frontmatter, body) from raw markdown.
// Returns zero MemoryFrontmatter if no frontmatter is present.
func ParseMemoryFrontmatter(raw string) (MemoryFrontmatter, string) {
	fields, _, body, err := parseFrontmatter(raw)
	if err != nil {
		return MemoryFrontmatter{}, raw
	}
	return MemoryFrontmatter{
		Name:        strings.TrimSpace(fields["name"]),
		Description: strings.TrimSpace(fields["description"]),
		Type:        ParseMemoryType(fields["type"]),
	}, body
}

// helper for filesystem.go cross-package test path; does nothing here.
var _ = os.Stat

// ── Memdir system-prompt section (Claude Code parity) ───────────────────────

// MemdirEntrypointFilename is the bootstrap memory index, loaded eagerly into
// the system prompt every turn. Contents follow Claude Code's contract: a
// concise list of pointers to typed memory files, no memory content directly.
const MemdirEntrypointFilename = "MEMORY.md"

// MaxMemdirEntrypointLines caps how many lines of MEMORY.md are spliced into
// the system prompt. Keeps token usage predictable when an index grows.
const MaxMemdirEntrypointLines = 200

// BuildMemdirPromptSection returns the static memdir behavioural instructions
// + the MEMORY.md content for each requested scope. Mirrors Claude Code's
// `buildMemoryPrompt` (memdir/memdir.ts:269) — the persistent piece that
// teaches the LLM the four-type taxonomy, what NOT to save, when to read,
// and how to write a new memory.
//
// Per-turn dynamic listings (manifest, freshness reminders) ride separately
// as <system-reminder> attachments via the memory tool's DynamicReminder
// hook — not in here.
//
// Returns "" when memory is empty across all scopes.
func BuildMemdirPromptSection(ctx context.Context, m MemoryProvider, scopes ...Scope) string {
	if m == nil {
		return ""
	}
	if len(scopes) == 0 {
		scopes = []Scope{ScopeUser, ScopeProject}
	}

	var b strings.Builder
	b.WriteString(memdirBehavioralPrompt)

	for _, s := range scopes {
		raw, err := m.View(ctx, s, "/"+MemdirEntrypointFilename)
		if err != nil {
			continue
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		raw = truncateLines(raw, MaxMemdirEntrypointLines)
		b.WriteString("\n\n## ")
		b.WriteString(MemdirEntrypointFilename)
		b.WriteString(" — ")
		b.WriteString(string(s))
		b.WriteString(" scope\n\n")
		b.WriteString(raw)
	}

	return b.String()
}

// truncateLines returns up to maxLines of s; appends a truncation note if
// content was dropped.
func truncateLines(s string, maxLines int) string {
	if maxLines <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	cut := lines[:maxLines]
	return strings.Join(cut, "\n") +
		fmt.Sprintf("\n\n[... %d more lines truncated — index too long, prune to keep MEMORY.md concise]", len(lines)-maxLines)
}

// memdirBehavioralPrompt is the static instructions block. Kept inline (not
// loaded from disk) so the SDK is self-contained and the prompt stays
// hash-stable across builds for prompt-cache fingerprinting.
const memdirBehavioralPrompt = `# Persistent memory

You have a file-based persistent memory system addressed via the **memory** tool.
It survives across conversations. Use it to remember facts, preferences,
corrections, and ongoing context that future conversations will benefit from.

## What memory is for (closed taxonomy)

Every memory file declares one of four types in its frontmatter:

- **user** — who the user is, their role, knowledge, preferences.
- **feedback** — corrections you received and validated approaches.
- **project** — ongoing work, deadlines, motivations specific to a project.
- **reference** — pointers to external systems, docs, dashboards.

Content that is derivable from the current code or repo state — architecture,
file:line citations, git history — is NOT memory. Read it from source.

## How to save a memory (two steps)

1. ` + "`memory(operation: \"create\", path: \"/<topic>.md\", scope: \"user|project\", type: \"<type>\", name: \"…\", description: \"one-line hook\", content: \"…\")`" + `
2. Add a one-line pointer in the scope's ` + "`/MEMORY.md`" + ` index:
   ` + "`- [Title](file.md) — one-line hook`" + ` (no frontmatter, ≤150 chars/line).

## When to access memory

- At the start of any non-trivial turn, scan the manifest in the
  <system-reminder> and read files that look relevant via ` + "`memory(view, …)`" + `.
- Before answering questions about the user, past decisions, or recurring
  preferences, check memory rather than guessing.

## Freshness

Every view returns the file's age. Memories older than a day may be stale —
verify against current state before asserting them as fact.

## Read-before-write contract

` + "`str_replace`" + ` requires a prior ` + "`view`" + ` of the same file in the same turn.
This prevents blind edits against stale content.`

