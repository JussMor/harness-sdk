// Package memory provides MemoryProvider implementations for the autobuild SDK.
package memory

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	autobuild "github.com/everfaz/autobuild-sdk"
)

// FilesystemMemory is a MemoryProvider backed by markdown files on disk.
// Two scopes (User, Project) live in two separate directories:
//
//	{Root}/user/    — cross-project user memory
//	{Root}/project/ — per-project memory (use a different Root per project)
//
// Paths inside the scope use forward slashes, mapped 1:1 to filesystem paths.
// E.g. View(ctx, ScopeUser, "/profile/work.md") reads {Root}/user/profile/work.md.
//
// Use this for:
//   - Local development without a database
//   - Single-user apps where data is portable as files
//   - Projects where memory should be reviewable in git diffs
type FilesystemMemory struct {
	Root string
	mu   sync.Mutex
}

// NewFilesystem creates a FilesystemMemory rooted at the given directory.
// Creates user/ and project/ subdirectories if missing.
func NewFilesystem(root string) (*FilesystemMemory, error) {
	if err := os.MkdirAll(filepath.Join(root, "user"), 0755); err != nil {
		return nil, fmt.Errorf("create user dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "project"), 0755); err != nil {
		return nil, fmt.Errorf("create project dir: %w", err)
	}
	return &FilesystemMemory{Root: root}, nil
}

func (m *FilesystemMemory) scopePath(scope autobuild.Scope, p string) string {
	clean := strings.TrimPrefix(p, "/")
	scopeDir := string(scope)
	return filepath.Join(m.Root, scopeDir, clean)
}

// View reads a file or, if the path is a directory, returns a listing.
func (m *FilesystemMemory) View(_ context.Context, scope autobuild.Scope, path string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	full := m.scopePath(scope, path)
	info, err := os.Stat(full)
	if os.IsNotExist(err) {
		return "", nil // empty memory is not an error
	}
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", full, err)
	}
	if info.IsDir() {
		return m.listDir(full)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", full, err)
	}
	return string(data), nil
}

func (m *FilesystemMemory) listDir(dir string) (string, error) {
	var b strings.Builder
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "." || strings.HasPrefix(rel, ".") {
			return nil
		}
		if info.IsDir() {
			b.WriteString(rel + "/\n")
			return nil
		}
		// Read and embed small markdown files inline
		if strings.HasSuffix(rel, ".md") && info.Size() < 4096 {
			data, _ := os.ReadFile(path)
			b.WriteString("# " + rel + "\n\n")
			b.Write(data)
			b.WriteString("\n\n")
		} else {
			b.WriteString(rel + "\n")
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk: %w", err)
	}
	return b.String(), nil
}

// Create writes a new file. Fails if the file already exists.
func (m *FilesystemMemory) Create(_ context.Context, scope autobuild.Scope, path string, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	full := m.scopePath(scope, path)
	if _, err := os.Stat(full); err == nil {
		return fmt.Errorf("file %s already exists", path)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(full, []byte(content), 0644)
}

// StrReplace performs an exact string replacement. oldStr must appear exactly once.
func (m *FilesystemMemory) StrReplace(_ context.Context, scope autobuild.Scope, path string, oldStr, newStr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	full := m.scopePath(scope, path)
	data, err := os.ReadFile(full)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	content := string(data)
	count := strings.Count(content, oldStr)
	if count == 0 {
		return fmt.Errorf("oldStr not found in %s", path)
	}
	if count > 1 {
		return fmt.Errorf("oldStr matches %d times in %s; must be unique", count, path)
	}
	updated := strings.Replace(content, oldStr, newStr, 1)
	return os.WriteFile(full, []byte(updated), 0644)
}

// Delete removes a file or directory.
func (m *FilesystemMemory) Delete(_ context.Context, scope autobuild.Scope, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return os.RemoveAll(m.scopePath(scope, path))
}

// Rename moves a file within the same scope.
func (m *FilesystemMemory) Rename(_ context.Context, scope autobuild.Scope, oldPath, newPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	oldFull := m.scopePath(scope, oldPath)
	newFull := m.scopePath(scope, newPath)
	if err := os.MkdirAll(filepath.Dir(newFull), 0755); err != nil {
		return err
	}
	return os.Rename(oldFull, newFull)
}

// List returns all file paths under the given directory.
func (m *FilesystemMemory) List(_ context.Context, scope autobuild.Scope, path string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir := m.scopePath(scope, path)
	var paths []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(filepath.Join(m.Root, string(scope)), p)
		paths = append(paths, "/"+filepath.ToSlash(rel))
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return paths, nil
}

// Search finds entries containing query terms, ranked by BM25 score.
// Falls back to substring match for single-word queries.
func (m *FilesystemMemory) Search(ctx context.Context, scope autobuild.Scope, query string) ([]autobuild.MemoryEntry, error) {
	paths, err := m.List(ctx, scope, "/")
	if err != nil {
		return nil, err
	}

	type candidate struct {
		entry autobuild.MemoryEntry
		score float64
	}

	queryTerms := tokenize(strings.ToLower(query))
	if len(queryTerms) == 0 {
		return nil, nil
	}

	// Collect all documents for IDF calculation
	type doc struct {
		path    string
		content string
		terms   []string
	}
	var docs []doc
	for _, p := range paths {
		content, err := m.View(ctx, scope, p)
		if err != nil || content == "" {
			continue
		}
		docs = append(docs, doc{
			path:    p,
			content: content,
			terms:   tokenize(strings.ToLower(content)),
		})
	}

	N := float64(len(docs))
	if N == 0 {
		return nil, nil
	}

	// BM25 parameters
	const k1 = 1.2
	const b = 0.75

	// Average document length
	var totalLen float64
	for _, d := range docs {
		totalLen += float64(len(d.terms))
	}
	avgdl := totalLen / N

	// Document frequency per query term
	df := make(map[string]int, len(queryTerms))
	for _, qt := range queryTerms {
		for _, d := range docs {
			for _, term := range d.terms {
				if term == qt {
					df[qt]++
					break
				}
			}
		}
	}

	// Score each document
	var candidates []candidate
	for _, d := range docs {
		// Term frequency map
		tf := make(map[string]int, len(d.terms))
		for _, term := range d.terms {
			tf[term]++
		}

		dl := float64(len(d.terms))
		var score float64
		for _, qt := range queryTerms {
			tfVal := float64(tf[qt])
			dfVal := float64(df[qt])
			if tfVal == 0 || dfVal == 0 {
				continue
			}
			idf := math.Log((N-dfVal+0.5)/(dfVal+0.5) + 1)
			numerator := tfVal * (k1 + 1)
			denominator := tfVal + k1*(1-b+b*dl/avgdl)
			score += idf * (numerator / denominator)
		}

		if score > 0 {
			candidates = append(candidates, candidate{
				entry: autobuild.MemoryEntry{
					Path:    d.path,
					Scope:   scope,
					Content: d.content,
				},
				score: score,
			})
		}
	}

	// Sort by score descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	results := make([]autobuild.MemoryEntry, 0, len(candidates))
	for _, c := range candidates {
		results = append(results, c.entry)
	}
	return results, nil
}

// tokenize splits text into lowercase words, stripping punctuation.
func tokenize(text string) []string {
	var terms []string
	for _, word := range strings.Fields(text) {
		word = strings.Trim(word, ".,!?;:\"'()[]{}") 
		if len(word) > 1 {
			terms = append(terms, word)
		}
	}
	return terms
}

var _ autobuild.MemoryProvider = (*FilesystemMemory)(nil)

// ── LayeredFilesystemMemory ───────────────────────────────────────────────────

// LayeredFilesystemMemory extends FilesystemMemory with layer-aware operations.
// Layer metadata is stored as a YAML-like frontmatter header in each file:
//
//	---
//	layer: explicit
//	confidence: 0.9
//	source: user
//	---
//	Actual content here
type LayeredFilesystemMemory struct {
	*FilesystemMemory
}

// NewLayeredFilesystem creates a LayeredFilesystemMemory.
func NewLayeredFilesystem(root string) (*LayeredFilesystemMemory, error) {
	fs, err := NewFilesystem(root)
	if err != nil {
		return nil, err
	}
	return &LayeredFilesystemMemory{FilesystemMemory: fs}, nil
}

func (m *LayeredFilesystemMemory) WriteLayered(ctx context.Context, scope autobuild.Scope, path string, content string, layer autobuild.MemoryLayer) error {
	full := "---\nlayer: " + string(layer) + "\n---\n" + content
	if err := m.Create(ctx, scope, path, full); err != nil {
		// If exists, update
		existing, readErr := m.View(ctx, scope, path)
		if readErr != nil {
			return err
		}
		return m.StrReplace(ctx, scope, path, existing, full)
	}
	return nil
}

func (m *LayeredFilesystemMemory) ReadLayered(ctx context.Context, scope autobuild.Scope, path string) (*autobuild.LayeredMemoryEntry, error) {
	content, err := m.View(ctx, scope, path)
	if err != nil {
		return nil, err
	}
	layer, clean := parseFrontmatter(content)
	return &autobuild.LayeredMemoryEntry{
		MemoryEntry: autobuild.MemoryEntry{
			Path:    path,
			Scope:   scope,
			Content: clean,
			Layer:   layer,
		},
		Layer:    layer,
		Priority: 0,
	}, nil
}

func (m *LayeredFilesystemMemory) SearchLayered(ctx context.Context, scope autobuild.Scope, query string) ([]autobuild.LayeredMemoryEntry, error) {
	entries, err := m.Search(ctx, scope, query)
	if err != nil {
		return nil, err
	}
	var layered []autobuild.LayeredMemoryEntry
	for _, e := range entries {
		layer, clean := parseFrontmatter(e.Content)
		e.Content = clean
		e.Layer = layer
		layered = append(layered, autobuild.LayeredMemoryEntry{
			MemoryEntry: e,
			Layer:       layer,
		})
	}
	autobuild.SortByPriority(layered)
	return layered, nil
}

func (m *LayeredFilesystemMemory) ClearSession(_ context.Context) error {
	// Session entries are not persisted to disk in FilesystemMemory —
	// they live in ObservationStore only. Nothing to clear here.
	return nil
}

// parseFrontmatter extracts layer from YAML frontmatter if present.
func parseFrontmatter(content string) (autobuild.MemoryLayer, string) {
	if !strings.HasPrefix(content, "---\n") {
		return autobuild.MemoryLayerInferred, content
	}
	end := strings.Index(content[4:], "\n---\n")
	if end < 0 {
		return autobuild.MemoryLayerInferred, content
	}
	header := content[4 : end+4]
	body := content[end+9:]
	var layer autobuild.MemoryLayer = autobuild.MemoryLayerInferred
	for _, line := range strings.Split(header, "\n") {
		if strings.HasPrefix(line, "layer: ") {
			layer = autobuild.MemoryLayer(strings.TrimPrefix(line, "layer: "))
		}
	}
	return layer, strings.TrimSpace(body)
}

var _ autobuild.LayeredMemoryProvider = (*LayeredFilesystemMemory)(nil)
