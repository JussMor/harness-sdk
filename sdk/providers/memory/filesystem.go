// Package memory provides MemoryProvider implementations for the autobuild SDK.
package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// Search finds entries containing a query string. Naive substring match.
// For semantic search, wrap with autobuild.SemanticObservationStore — or
// implement a separate search index.
func (m *FilesystemMemory) Search(ctx context.Context, scope autobuild.Scope, query string) ([]autobuild.MemoryEntry, error) {
	paths, err := m.List(ctx, scope, "/")
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	var results []autobuild.MemoryEntry
	for _, p := range paths {
		content, err := m.View(ctx, scope, p)
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(content), q) {
			results = append(results, autobuild.MemoryEntry{
				Path:    p,
				Scope:   scope,
				Content: content,
			})
		}
	}
	return results, nil
}

var _ autobuild.MemoryProvider = (*FilesystemMemory)(nil)
