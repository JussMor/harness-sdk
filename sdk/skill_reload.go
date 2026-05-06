package autobuild

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// osStat is a tiny indirection so tests can override it.
var osStat = os.Stat

// SkillReloader watches a skills directory for changes and reloads skills
// automatically when SKILL.md files are added, modified, or removed.
//
// Implementation note: uses mtime-based polling instead of inotify/fsnotify
// to avoid platform-specific dependencies. Default poll interval is 5 seconds.
//
// Usage:
//
//	skills, _ := autobuild.LoadSkillsDir("./skills")
//	provider := backend.NewSkillProvider(skills)
//	reloader := autobuild.NewSkillReloader("./skills", provider)
//	reloader.Start(ctx)
//	defer reloader.Stop()
type SkillReloader struct {
	dir          string
	provider     ReloadableSkillProvider
	pollInterval time.Duration

	mu       sync.Mutex
	hashes   map[string]string // path → content hash
	stopCh   chan struct{}
	doneCh   chan struct{}
	onReload func(loaded, removed []string)
}

// ReloadableSkillProvider is a SkillProvider that supports adding/removing
// skills at runtime. Most production providers should implement this.
type ReloadableSkillProvider interface {
	SkillProvider
	// AddOrReplace adds a new skill or replaces an existing one with the same Meta.ID.
	AddOrReplace(skill *Skill)
	// Remove removes a skill by ID.
	Remove(skillID string)
}

// NewSkillReloader creates a watcher for the given directory.
// Default poll interval is 5 seconds — set Reloader.PollInterval before Start
// to customize.
func NewSkillReloader(dir string, provider ReloadableSkillProvider) *SkillReloader {
	return &SkillReloader{
		dir:          dir,
		provider:     provider,
		pollInterval: 5 * time.Second,
		hashes:       make(map[string]string),
	}
}

// SetPollInterval sets how often the reloader checks for changes.
// Must be called before Start.
func (r *SkillReloader) SetPollInterval(d time.Duration) {
	r.pollInterval = d
}

// SetOnReload sets a callback fired after each reload cycle with the
// IDs of loaded and removed skills.
func (r *SkillReloader) SetOnReload(fn func(loaded, removed []string)) {
	r.onReload = fn
}

// Start begins watching the directory in a background goroutine.
// Returns immediately. Call Stop to cancel.
func (r *SkillReloader) Start(ctx context.Context) {
	r.mu.Lock()
	if r.stopCh != nil {
		r.mu.Unlock()
		return // already running
	}
	r.stopCh = make(chan struct{})
	r.doneCh = make(chan struct{})
	r.mu.Unlock()

	// Initial scan to populate hashes
	r.scan()

	go r.loop(ctx)
}

// Stop terminates the background goroutine. Safe to call multiple times.
func (r *SkillReloader) Stop() {
	r.mu.Lock()
	if r.stopCh == nil {
		r.mu.Unlock()
		return
	}
	close(r.stopCh)
	doneCh := r.doneCh
	r.stopCh = nil
	r.mu.Unlock()
	<-doneCh
}

func (r *SkillReloader) loop(ctx context.Context) {
	defer close(r.doneCh)
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.scan()
		}
	}
}

// scan walks the skills directory and detects added/changed/removed files.
func (r *SkillReloader) scan() {
	currentFiles := make(map[string]string) // path → hash
	_ = filepath.WalkDir(r.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(path) != "SKILL.md" {
			return nil
		}
		hash, err := hashFile(path)
		if err != nil {
			return nil
		}
		currentFiles[path] = hash
		return nil
	})

	r.mu.Lock()
	defer r.mu.Unlock()

	var loaded []string
	var removed []string

	// Detect added or changed
	for path, hash := range currentFiles {
		if r.hashes[path] != hash {
			skill, err := loadSkillFromFile(path)
			if err == nil && skill != nil {
				r.provider.AddOrReplace(skill)
				loaded = append(loaded, skill.Meta.Name)
			}
			r.hashes[path] = hash
		}
	}

	// Detect removed
	for path := range r.hashes {
		if _, exists := currentFiles[path]; !exists {
			id := skillIDFromPath(path)
			r.provider.Remove(id)
			removed = append(removed, id)
			delete(r.hashes, path)
		}
	}

	if r.onReload != nil && (len(loaded) > 0 || len(removed) > 0) {
		r.onReload(loaded, removed)
	}
}

// loadSkillFromFile parses a single SKILL.md file.
func loadSkillFromFile(path string) (*Skill, error) {
	skills, err := LoadSkillsDir(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if len(skills) == 0 {
		return nil, nil
	}
	return skills[0], nil
}

// skillIDFromPath derives a skill ID from its file path.
// Uses the parent directory name (e.g. /skills/code-review/SKILL.md → "code-review").
func skillIDFromPath(path string) string {
	return filepath.Base(filepath.Dir(path))
}

// hashFile returns a SHA-1 hash of the file's mtime + size — fast and avoids
// reading the file contents on every poll.
func hashFile(path string) (string, error) {
	info, err := osStat(path)
	if err != nil {
		return "", err
	}
	h := sha1.New()
	h.Write([]byte(info.ModTime().String()))
	h.Write([]byte{byte(info.Size())})
	return hex.EncodeToString(h.Sum(nil)), nil
}
