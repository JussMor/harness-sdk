package autobuild

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// inMemMemoryProvider — in-memory MemoryProvider + MemoryStater for tests.
//
// Mirrors filesystem semantics closely enough to exercise the memory tool:
//   - View("") and View("/") return a directory listing of the scope root
//   - View on an existing file returns its content
//   - View on a non-existent path returns an error
//   - Create errors when file already exists
//   - Stat reports IsDir=true for "" / "/" and IsDir=false for files
//
// We deliberately reproduce the View-on-empty-path behaviour because that is
// the exact bug the memory tool now defends against (empty path + non-empty
// listing being misread as "file already exists").
// ─────────────────────────────────────────────────────────────────────────────

type inMemMemoryProvider struct {
	mu      sync.Mutex
	files   map[Scope]map[string]string // path → content
	mtimeMs map[Scope]map[string]int64
	clock   int64 // monotonic counter, incremented on every write
}

func newInMemMemoryProvider() *inMemMemoryProvider {
	return &inMemMemoryProvider{
		files:   map[Scope]map[string]string{ScopeUser: {}, ScopeProject: {}},
		mtimeMs: map[Scope]map[string]int64{ScopeUser: {}, ScopeProject: {}},
		clock:   time.Now().UnixMilli(),
	}
}

func (m *inMemMemoryProvider) tick() int64 {
	m.clock++
	return m.clock
}

func normPath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

func (m *inMemMemoryProvider) View(_ context.Context, scope Scope, path string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bucket := m.files[scope]
	if bucket == nil {
		return "", errors.New("scope not found")
	}
	p := normPath(path)
	// Directory listing for root or any path ending with "/"
	if p == "/" || strings.HasSuffix(p, "/") {
		var names []string
		prefix := p
		for f := range bucket {
			if strings.HasPrefix(f, prefix) {
				rest := strings.TrimPrefix(f, prefix)
				if rest == "" {
					continue
				}
				// Only direct children (no further "/" considered for tests).
				names = append(names, rest)
			}
		}
		sort.Strings(names)
		return strings.Join(names, "\n"), nil
	}
	if v, ok := bucket[p]; ok {
		return v, nil
	}
	return "", fmt.Errorf("not found: %s", p)
}

func (m *inMemMemoryProvider) Create(_ context.Context, scope Scope, path, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	bucket := m.files[scope]
	p := normPath(path)
	if _, ok := bucket[p]; ok {
		return fmt.Errorf("already exists: %s", p)
	}
	bucket[p] = content
	m.mtimeMs[scope][p] = m.tick()
	return nil
}

func (m *inMemMemoryProvider) StrReplace(_ context.Context, scope Scope, path, oldStr, newStr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	bucket := m.files[scope]
	p := normPath(path)
	cur, ok := bucket[p]
	if !ok {
		return fmt.Errorf("not found: %s", p)
	}
	if oldStr == "" {
		// emulate "replace whole file" semantics used by maybeAppendMemoryIndex
		bucket[p] = newStr
		m.mtimeMs[scope][p] = m.tick()
		return nil
	}
	if !strings.Contains(cur, oldStr) {
		return fmt.Errorf("oldStr not found in %s", p)
	}
	bucket[p] = strings.Replace(cur, oldStr, newStr, 1)
	m.mtimeMs[scope][p] = m.tick()
	return nil
}

func (m *inMemMemoryProvider) Delete(_ context.Context, scope Scope, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	bucket := m.files[scope]
	p := normPath(path)
	if _, ok := bucket[p]; !ok {
		return fmt.Errorf("not found: %s", p)
	}
	delete(bucket, p)
	delete(m.mtimeMs[scope], p)
	return nil
}

func (m *inMemMemoryProvider) Rename(_ context.Context, scope Scope, oldPath, newPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	bucket := m.files[scope]
	op := normPath(oldPath)
	np := normPath(newPath)
	cur, ok := bucket[op]
	if !ok {
		return fmt.Errorf("not found: %s", op)
	}
	if _, exists := bucket[np]; exists {
		return fmt.Errorf("already exists: %s", np)
	}
	bucket[np] = cur
	delete(bucket, op)
	m.mtimeMs[scope][np] = m.tick()
	delete(m.mtimeMs[scope], op)
	return nil
}

func (m *inMemMemoryProvider) List(_ context.Context, scope Scope, path string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bucket := m.files[scope]
	prefix := normPath(path)
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	var out []string
	for f := range bucket {
		if prefix == "/" || strings.HasPrefix(f, prefix) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (m *inMemMemoryProvider) Search(_ context.Context, scope Scope, query string) ([]MemoryEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bucket := m.files[scope]
	var out []MemoryEntry
	q := strings.ToLower(query)
	for p, c := range bucket {
		if q == "" || strings.Contains(strings.ToLower(c), q) || strings.Contains(strings.ToLower(p), q) {
			out = append(out, MemoryEntry{Path: p, Scope: scope, Content: c})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (m *inMemMemoryProvider) Stat(_ context.Context, scope Scope, path string) (MemoryStat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := normPath(path)
	bucket := m.files[scope]
	if v, ok := bucket[p]; ok {
		return MemoryStat{MtimeMs: m.mtimeMs[scope][p], Size: int64(len(v))}, nil
	}
	if p == "/" || strings.HasSuffix(p, "/") {
		return MemoryStat{IsDir: true}, nil
	}
	// Match a "directory" if any file lives under p+"/".
	for f := range bucket {
		if strings.HasPrefix(f, p+"/") {
			return MemoryStat{IsDir: true}, nil
		}
	}
	return MemoryStat{}, fmt.Errorf("not found: %s", p)
}

// Compile-time interface checks.
var _ MemoryProvider = (*inMemMemoryProvider)(nil)
var _ MemoryStater = (*inMemMemoryProvider)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func newTestMemoryTool(t *testing.T) (*Tool, *inMemMemoryProvider) {
	t.Helper()
	p := newInMemMemoryProvider()
	tool := NewMemoryTool(MemoryToolConfig{
		Provider:     p,
		DefaultScope: ScopeProject,
	})
	if tool == nil {
		t.Fatal("NewMemoryTool returned nil")
	}
	return tool, p
}

func runOp(t *testing.T, tool *Tool, args map[string]any) (string, error) {
	t.Helper()
	return tool.Execute(context.Background(), "", args)
}

func mustOp(t *testing.T, tool *Tool, args map[string]any) string {
	t.Helper()
	out, err := runOp(t, tool, args)
	if err != nil {
		t.Fatalf("op %v failed: %v", args, err)
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// create
// ─────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_Create_RequiresPath(t *testing.T) {
	tool, _ := newTestMemoryTool(t)
	_, err := runOp(t, tool, map[string]any{
		"operation": "create",
		"scope":     "user",
		"type":      "user",
		"name":      "Preferences",
		"content":   "Go + concise",
	})
	if err == nil {
		t.Fatal("expected error for missing path")
	}
	if !strings.Contains(err.Error(), "'path' is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMemoryTool_Create_RejectsDirectoryPath(t *testing.T) {
	tool, _ := newTestMemoryTool(t)
	_, err := runOp(t, tool, map[string]any{
		"operation": "create",
		"scope":     "user",
		"path":      "/feedback/",
		"type":      "feedback",
		"content":   "x",
	})
	if err == nil || !strings.Contains(err.Error(), "must point to a file") {
		t.Fatalf("want directory-path rejection, got: %v", err)
	}
}

func TestMemoryTool_Create_RequiresType(t *testing.T) {
	tool, _ := newTestMemoryTool(t)
	_, err := runOp(t, tool, map[string]any{
		"operation": "create",
		"scope":     "user",
		"path":      "/preferences.md",
		"content":   "x",
	})
	if err == nil || !strings.Contains(err.Error(), "'type' is required") {
		t.Fatalf("want missing-type error, got: %v", err)
	}
}

func TestMemoryTool_Create_HappyPath_AutoSeedsIndex(t *testing.T) {
	tool, p := newTestMemoryTool(t)
	out := mustOp(t, tool, map[string]any{
		"operation":   "create",
		"scope":       "user",
		"path":        "/preferences.md",
		"type":        "user",
		"name":        "Preferences",
		"description": "Go + respuestas concisas",
		"content":     "User prefers Go.",
	})
	if !strings.Contains(out, "created /preferences.md [user]") {
		t.Errorf("missing create confirmation: %q", out)
	}
	if !strings.Contains(out, "MEMORY.md created") {
		t.Errorf("expected MEMORY.md auto-seed signal, got: %q", out)
	}

	idx, err := p.View(context.Background(), ScopeUser, "/MEMORY.md")
	if err != nil {
		t.Fatalf("MEMORY.md not created: %v", err)
	}
	if !strings.Contains(idx, "[Preferences](preferences.md)") {
		t.Errorf("index missing pointer line: %q", idx)
	}
	if !strings.Contains(idx, "Go + respuestas concisas") {
		t.Errorf("index missing hook: %q", idx)
	}
}

func TestMemoryTool_Create_AppendsToExistingIndex(t *testing.T) {
	tool, p := newTestMemoryTool(t)
	mustOp(t, tool, map[string]any{
		"operation":   "create",
		"scope":       "user",
		"path":        "/preferences.md",
		"type":        "user",
		"name":        "Preferences",
		"description": "First entry",
		"content":     "x",
	})
	out := mustOp(t, tool, map[string]any{
		"operation":   "create",
		"scope":       "user",
		"path":        "/feedback/testing.md",
		"type":        "feedback",
		"name":        "Testing",
		"description": "Always table-test",
		"content":     "y",
	})
	if !strings.Contains(out, "index updated") {
		t.Errorf("expected index-updated signal, got: %q", out)
	}
	idx, _ := p.View(context.Background(), ScopeUser, "/MEMORY.md")
	if !strings.Contains(idx, "[Preferences](preferences.md)") ||
		!strings.Contains(idx, "[Testing](feedback/testing.md)") {
		t.Errorf("index missing entries: %q", idx)
	}
}

func TestMemoryTool_Create_SkipsIndexUpdateForMemoryMd(t *testing.T) {
	tool, p := newTestMemoryTool(t)
	out := mustOp(t, tool, map[string]any{
		"operation": "create",
		"scope":     "user",
		"path":      "/MEMORY.md",
		"type":      "user",
		"name":      "Index",
		"content":   "# index",
	})
	if strings.Contains(out, "index updated") || strings.Contains(out, "MEMORY.md created") {
		t.Errorf("MEMORY.md self-create should not trigger index-update signal: %q", out)
	}
	body, _ := p.View(context.Background(), ScopeUser, "/MEMORY.md")
	if !strings.Contains(body, "# index") {
		t.Errorf("body not preserved: %q", body)
	}
}

func TestMemoryTool_Create_DuplicatePath(t *testing.T) {
	tool, _ := newTestMemoryTool(t)
	mustOp(t, tool, map[string]any{
		"operation": "create",
		"scope":     "user",
		"path":      "/preferences.md",
		"type":      "user",
		"name":      "P",
		"content":   "x",
	})
	_, err := runOp(t, tool, map[string]any{
		"operation": "create",
		"scope":     "user",
		"path":      "/preferences.md",
		"type":      "user",
		"name":      "P",
		"content":   "y",
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want already-exists error, got: %v", err)
	}
}

// Reproduces the original bug: empty path used to View("") which returned a
// non-empty directory listing and was misread as "file exists". With the
// stater-first existence check + path validation, this must now error on
// missing path — never on a phantom collision.
func TestMemoryTool_Create_EmptyPath_DoesNotConfusePopulatedScope(t *testing.T) {
	tool, p := newTestMemoryTool(t)
	// Pre-populate scope so the directory listing is non-empty.
	if err := p.Create(context.Background(), ScopeUser, "/seed.md", "x"); err != nil {
		t.Fatal(err)
	}
	_, err := runOp(t, tool, map[string]any{
		"operation": "create",
		"scope":     "user",
		"type":      "user",
		"name":      "X",
		"content":   "y",
		// no "path" key
	})
	if err == nil {
		t.Fatal("expected path-required error, got nil")
	}
	if strings.Contains(err.Error(), "already exists") {
		t.Fatalf("regression: empty path produced phantom collision: %v", err)
	}
	if !strings.Contains(err.Error(), "'path' is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMemoryTool_Create_AntiMerge_DoesNotApplyOnFreshFile(t *testing.T) {
	// Anti-merge is for str_replace; create on a fresh path should succeed
	// regardless of other files of different types.
	tool, _ := newTestMemoryTool(t)
	mustOp(t, tool, map[string]any{
		"operation": "create", "scope": "user",
		"path": "/preferences.md", "type": "user", "name": "P", "content": "x",
	})
	_, err := runOp(t, tool, map[string]any{
		"operation": "create", "scope": "user",
		"path": "/feedback/testing.md", "type": "feedback", "name": "T", "content": "y",
	})
	if err != nil {
		t.Fatalf("unexpected error creating second file: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// view + read-before-write
// ─────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_View_Empty(t *testing.T) {
	tool, _ := newTestMemoryTool(t)
	out := mustOp(t, tool, map[string]any{
		"operation": "view", "scope": "user", "path": "/",
	})
	// Empty user-scope root → "(empty)" sentinel.
	if !strings.Contains(out, "(empty)") {
		t.Errorf("expected empty marker, got: %q", out)
	}
}

func TestMemoryTool_View_File(t *testing.T) {
	tool, _ := newTestMemoryTool(t)
	mustOp(t, tool, map[string]any{
		"operation": "create", "scope": "user",
		"path": "/preferences.md", "type": "user", "name": "P",
		"content": "hello",
	})
	out := mustOp(t, tool, map[string]any{
		"operation": "view", "scope": "user", "path": "/preferences.md",
	})
	if !strings.Contains(out, "hello") {
		t.Errorf("view output missing body: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// str_replace
// ─────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_StrReplace_RequiresPath(t *testing.T) {
	tool, _ := newTestMemoryTool(t)
	_, err := runOp(t, tool, map[string]any{
		"operation": "str_replace", "scope": "user",
		"oldStr": "x", "newStr": "y",
	})
	if err == nil || !strings.Contains(err.Error(), "'path' is required") {
		t.Fatalf("want path-required error, got: %v", err)
	}
}

func TestMemoryTool_StrReplace_RequiresPriorView(t *testing.T) {
	tool, p := newTestMemoryTool(t)
	// Seed file directly in provider without going through the tool — the
	// read-before-write tracker therefore has no record of it.
	if err := p.Create(context.Background(), ScopeUser, "/preferences.md", "hello"); err != nil {
		t.Fatal(err)
	}
	_, err := runOp(t, tool, map[string]any{
		"operation": "str_replace", "scope": "user",
		"path": "/preferences.md", "oldStr": "hello", "newStr": "world",
	})
	if err == nil {
		t.Fatal("expected freshness error, got nil")
	}
}

func TestMemoryTool_StrReplace_HappyPath(t *testing.T) {
	tool, p := newTestMemoryTool(t)
	mustOp(t, tool, map[string]any{
		"operation": "create", "scope": "user",
		"path": "/preferences.md", "type": "user", "name": "P",
		"content": "Go is preferred.",
	})
	// view to satisfy read-before-write
	mustOp(t, tool, map[string]any{
		"operation": "view", "scope": "user", "path": "/preferences.md",
	})
	mustOp(t, tool, map[string]any{
		"operation": "str_replace", "scope": "user",
		"path": "/preferences.md",
		"oldStr": "Go is preferred.", "newStr": "Go and concise responses.",
	})
	cur, _ := p.View(context.Background(), ScopeUser, "/preferences.md")
	if !strings.Contains(cur, "Go and concise responses.") {
		t.Errorf("str_replace did not apply: %q", cur)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// delete + rename
// ─────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_Delete_RequiresPath(t *testing.T) {
	tool, _ := newTestMemoryTool(t)
	_, err := runOp(t, tool, map[string]any{
		"operation": "delete", "scope": "user",
	})
	if err == nil || !strings.Contains(err.Error(), "'path' is required") {
		t.Fatalf("want path-required error, got: %v", err)
	}
}

func TestMemoryTool_Delete_RequiresPriorView(t *testing.T) {
	tool, p := newTestMemoryTool(t)
	if err := p.Create(context.Background(), ScopeUser, "/x.md", "x"); err != nil {
		t.Fatal(err)
	}
	_, err := runOp(t, tool, map[string]any{
		"operation": "delete", "scope": "user", "path": "/x.md",
	})
	if err == nil || !strings.Contains(err.Error(), "view it before deleting") {
		t.Fatalf("want read-before-delete error, got: %v", err)
	}
}

func TestMemoryTool_Delete_HappyPath(t *testing.T) {
	tool, p := newTestMemoryTool(t)
	mustOp(t, tool, map[string]any{
		"operation": "create", "scope": "user",
		"path": "/x.md", "type": "user", "name": "X", "content": "x",
	})
	mustOp(t, tool, map[string]any{
		"operation": "view", "scope": "user", "path": "/x.md",
	})
	mustOp(t, tool, map[string]any{
		"operation": "delete", "scope": "user", "path": "/x.md",
	})
	if _, err := p.View(context.Background(), ScopeUser, "/x.md"); err == nil {
		t.Error("file should be deleted")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// list / search / find_relevant
// ─────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_List(t *testing.T) {
	tool, _ := newTestMemoryTool(t)
	mustOp(t, tool, map[string]any{
		"operation": "create", "scope": "user",
		"path": "/a.md", "type": "user", "name": "A", "content": "x",
	})
	mustOp(t, tool, map[string]any{
		"operation": "create", "scope": "user",
		"path": "/b.md", "type": "user", "name": "B", "content": "y",
	})
	out := mustOp(t, tool, map[string]any{
		"operation": "list", "scope": "user", "path": "/",
	})
	if !strings.Contains(out, "/a.md") || !strings.Contains(out, "/b.md") {
		t.Errorf("list missing entries: %q", out)
	}
}

func TestMemoryTool_Search(t *testing.T) {
	tool, _ := newTestMemoryTool(t)
	mustOp(t, tool, map[string]any{
		"operation": "create", "scope": "user",
		"path": "/preferences.md", "type": "user", "name": "P",
		"content": "User prefers Go and concise answers.",
	})
	out := mustOp(t, tool, map[string]any{
		"operation": "search", "scope": "user", "query": "concise",
	})
	if !strings.Contains(out, "preferences.md") {
		t.Errorf("search missed match: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// scope guards
// ─────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_Create_RejectsDisallowedScope(t *testing.T) {
	p := newInMemMemoryProvider()
	tool := NewMemoryTool(MemoryToolConfig{
		Provider:      p,
		AllowedScopes: []Scope{ScopeProject}, // user not allowed
	})
	_, err := runOp(t, tool, map[string]any{
		"operation": "create", "scope": "user",
		"path": "/x.md", "type": "user", "name": "X", "content": "x",
	})
	if err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("want scope-not-writable error, got: %v", err)
	}
}
