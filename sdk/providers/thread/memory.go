// Package thread provides ThreadProvider implementations for the autobuild SDK.
// A Thread is the persistence unit that links Conversations to projects —
// one Thread can host multiple Conversations over time (e.g. a long-running
// project assistant where the user starts a new chat each day).
package thread

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	autobuild "github.com/everfaz/autobuild-sdk"
)

// ── InMemoryThreadProvider ────────────────────────────────────────────────────

// InMemoryThreadProvider stores threads in memory.
// Safe for concurrent use. State is lost on process restart.
// Use FilesystemThreadProvider for persistence.
type InMemoryThreadProvider struct {
	mu      sync.RWMutex
	threads map[string]*autobuild.Thread
	inbox   map[string][]autobuild.Message // threadID → messages
}

// NewInMemory returns a ready-to-use in-memory ThreadProvider.
func NewInMemory() *InMemoryThreadProvider {
	return &InMemoryThreadProvider{
		threads: make(map[string]*autobuild.Thread),
		inbox:   make(map[string][]autobuild.Message),
	}
}

func (p *InMemoryThreadProvider) Create(_ context.Context, projectID, modeID string) (*autobuild.Thread, error) {
	t := &autobuild.Thread{
		ID:        newID("th"),
		ProjectID: projectID,
		ModeID:    modeID,
		Status:    autobuild.ThreadStatusActive,
	}
	p.mu.Lock()
	p.threads[t.ID] = t
	p.mu.Unlock()
	return t, nil
}

func (p *InMemoryThreadProvider) Get(_ context.Context, threadID string) (*autobuild.Thread, error) {
	p.mu.RLock()
	t, ok := p.threads[threadID]
	p.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("thread %q not found", threadID)
	}
	cp := *t
	return &cp, nil
}

func (p *InMemoryThreadProvider) Archive(_ context.Context, threadID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.threads[threadID]
	if !ok {
		return fmt.Errorf("thread %q not found", threadID)
	}
	t.Status = autobuild.ThreadStatusArchived
	return nil
}

func (p *InMemoryThreadProvider) SendMessage(_ context.Context, msg autobuild.Message) error {
	p.mu.Lock()
	p.inbox[msg.ToThreadID] = append(p.inbox[msg.ToThreadID], msg)
	p.mu.Unlock()
	return nil
}

// Inbox returns all messages received by a thread.
func (p *InMemoryThreadProvider) Inbox(_ context.Context, threadID string) ([]autobuild.Message, error) {
	p.mu.RLock()
	msgs := p.inbox[threadID]
	p.mu.RUnlock()
	out := make([]autobuild.Message, len(msgs))
	copy(out, msgs)
	return out, nil
}

// List returns all threads, optionally filtered by projectID (empty = all).
func (p *InMemoryThreadProvider) List(_ context.Context, projectID string) ([]*autobuild.Thread, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []*autobuild.Thread
	for _, t := range p.threads {
		if projectID == "" || t.ProjectID == projectID {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}

// Verify interface at compile time.
var _ autobuild.ThreadProvider = (*InMemoryThreadProvider)(nil)

// ── FilesystemThreadProvider ──────────────────────────────────────────────────

// FilesystemThreadProvider persists threads as JSON files under a root directory.
// Structure: <root>/threads/<threadID>.json
//
// Safe for concurrent use within one process. Not safe for multi-process access.
type FilesystemThreadProvider struct {
	root string
	mem  *InMemoryThreadProvider // write-through cache
	once sync.Once
}

// NewFilesystem returns a provider that persists threads to disk.
// dir is the root directory (created if it doesn't exist).
func NewFilesystem(dir string) (*FilesystemThreadProvider, error) {
	if err := os.MkdirAll(filepath.Join(dir, "threads"), 0o755); err != nil {
		return nil, fmt.Errorf("thread filesystem: %w", err)
	}
	p := &FilesystemThreadProvider{
		root: dir,
		mem:  NewInMemory(),
	}
	// Load existing threads from disk into cache
	if err := p.load(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *FilesystemThreadProvider) load() error {
	dir := filepath.Join(p.root, "threads")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var t autobuild.Thread
		if err := json.Unmarshal(data, &t); err != nil {
			continue
		}
		p.mem.mu.Lock()
		p.mem.threads[t.ID] = &t
		p.mem.mu.Unlock()
	}
	return nil
}

func (p *FilesystemThreadProvider) save(t *autobuild.Thread) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(p.root, "threads", t.ID+".json")
	return os.WriteFile(path, data, 0o644)
}

func (p *FilesystemThreadProvider) Create(ctx context.Context, projectID, modeID string) (*autobuild.Thread, error) {
	t, err := p.mem.Create(ctx, projectID, modeID)
	if err != nil {
		return nil, err
	}
	if err := p.save(t); err != nil {
		return nil, fmt.Errorf("thread filesystem save: %w", err)
	}
	return t, nil
}

func (p *FilesystemThreadProvider) Get(ctx context.Context, threadID string) (*autobuild.Thread, error) {
	return p.mem.Get(ctx, threadID)
}

func (p *FilesystemThreadProvider) Archive(ctx context.Context, threadID string) error {
	if err := p.mem.Archive(ctx, threadID); err != nil {
		return err
	}
	t, _ := p.mem.Get(ctx, threadID)
	if t != nil {
		_ = p.save(t)
	}
	return nil
}

func (p *FilesystemThreadProvider) SendMessage(ctx context.Context, msg autobuild.Message) error {
	return p.mem.SendMessage(ctx, msg)
}

var _ autobuild.ThreadProvider = (*FilesystemThreadProvider)(nil)

// ── helpers ──────────────────────────────────────────────────────────────────

func newID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
