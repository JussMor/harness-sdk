package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ab "github.com/everfaz/autobuild-sdk"
)

type fsMemoryProvider struct {
	root string
}

func newFSMemoryProvider(root string) (*fsMemoryProvider, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("memory root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(absRoot, string(ab.ScopeUser)), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(absRoot, string(ab.ScopeProject)), 0o755); err != nil {
		return nil, err
	}
	return &fsMemoryProvider{root: absRoot}, nil
}

func loadBackendMemory() (ab.MemoryProvider, error) {
	paths := []string{
		"memory",
		filepath.Join("example", "backend-chat", "memory"),
		filepath.Join("..", "backend-chat", "memory"),
	}

	var lastErr error
	for _, p := range paths {
		provider, err := newFSMemoryProvider(p)
		if err == nil {
			return provider, nil
		}
		lastErr = err
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("unable to initialize backend memory provider")
}

func (m *fsMemoryProvider) View(_ context.Context, scope ab.Scope, path string) (string, error) {
	if scope == ab.Scope("*") {
		userOut, err := m.View(context.Background(), ab.ScopeUser, path)
		if err != nil {
			userOut = fmt.Sprintf("error: %v", err)
		}
		projectOut, err := m.View(context.Background(), ab.ScopeProject, path)
		if err != nil {
			projectOut = fmt.Sprintf("error: %v", err)
		}
		return "[user]\n" + userOut + "\n\n[project]\n" + projectOut, nil
	}

	target, err := m.resolve(scope, path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}

	if info.IsDir() {
		entries, err := os.ReadDir(target)
		if err != nil {
			return "", err
		}
		items := make([]string, 0, len(entries))
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			items = append(items, name)
		}
		sort.Strings(items)
		if len(items) == 0 {
			return "", nil
		}
		return strings.Join(items, "\n"), nil
	}

	data, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (m *fsMemoryProvider) Create(_ context.Context, scope ab.Scope, path, content string) error {
	target, err := m.resolve(scope, path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("memory file already exists: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, []byte(content), 0o644)
}

func (m *fsMemoryProvider) StrReplace(_ context.Context, scope ab.Scope, path, oldStr, newStr string) error {
	target, err := m.resolve(scope, path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	text := string(data)
	count := strings.Count(text, oldStr)
	if count != 1 {
		return fmt.Errorf("old_str must appear exactly once (found %d)", count)
	}
	text = strings.Replace(text, oldStr, newStr, 1)
	return os.WriteFile(target, []byte(text), 0o644)
}

func (m *fsMemoryProvider) Delete(_ context.Context, scope ab.Scope, path string) error {
	target, err := m.resolve(scope, path)
	if err != nil {
		return err
	}
	return os.RemoveAll(target)
}

func (m *fsMemoryProvider) Rename(_ context.Context, scope ab.Scope, oldPath, newPath string) error {
	oldTarget, err := m.resolve(scope, oldPath)
	if err != nil {
		return err
	}
	newTarget, err := m.resolve(scope, newPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(newTarget), 0o755); err != nil {
		return err
	}
	return os.Rename(oldTarget, newTarget)
}

func (m *fsMemoryProvider) List(_ context.Context, scope ab.Scope, path string) ([]string, error) {
	target, err := m.resolve(scope, path)
	if err != nil {
		return nil, err
	}

	items := make([]string, 0)
	err = filepath.WalkDir(target, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(target, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			rel += "/"
		}
		items = append(items, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(items)
	return items, nil
}

func (m *fsMemoryProvider) Search(_ context.Context, scope ab.Scope, query string) ([]ab.MemoryEntry, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, fmt.Errorf("search query is required")
	}

	searchScopes := []ab.Scope{scope}
	if scope == ab.Scope("*") {
		searchScopes = []ab.Scope{ab.ScopeUser, ab.ScopeProject}
	}

	out := make([]ab.MemoryEntry, 0)
	for _, sc := range searchScopes {
		root, err := m.resolve(sc, ".")
		if err != nil {
			return nil, err
		}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			content := string(data)
			if !strings.Contains(strings.ToLower(content), query) && !strings.Contains(strings.ToLower(filepath.Base(p)), query) {
				return nil
			}
			rel, err := filepath.Rel(filepath.Join(m.root, string(sc)), p)
			if err != nil {
				return nil
			}
			out = append(out, ab.MemoryEntry{
				Path:    filepath.ToSlash(rel),
				Scope:   sc,
				Content: content,
			})
			return nil
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope == out[j].Scope {
			return out[i].Path < out[j].Path
		}
		return out[i].Scope < out[j].Scope
	})
	return out, nil
}

func (m *fsMemoryProvider) resolve(scope ab.Scope, p string) (string, error) {
	if scope != ab.ScopeUser && scope != ab.ScopeProject {
		return "", fmt.Errorf("invalid memory scope: %s", scope)
	}

	clean := filepath.Clean(strings.TrimSpace(p))
	if clean == "" {
		clean = "."
	}
	base := filepath.Join(m.root, string(scope))
	target := filepath.Join(base, clean)
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	if absTarget != absBase && !strings.HasPrefix(absTarget, absBase+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid memory path: %s", p)
	}
	return absTarget, nil
}
