// Package store provides ConversationStore implementations for the autobuild SDK.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	autobuild "github.com/everfaz/autobuild-sdk"
)

// FilesystemStore persists conversations as JSON files on disk.
// Each conversation is stored at {Root}/{conversationID}.json.
//
// Suitable for:
//   - Single-user apps (CLI tools, personal assistants)
//   - Development and testing with durable state
//   - Scenarios where you want conversations reviewable as files
//
// Not suitable for:
//   - Multi-user concurrent access (no row-level locking)
//   - High-write workloads (each Save rewrites the full file)
//
// For those cases, use a SQLite or Postgres store.
type FilesystemStore struct {
	Root string // directory where conversation files are stored
}

// NewFilesystem creates a FilesystemStore rooted at the given directory.
// Creates the directory if it doesn't exist.
func NewFilesystem(root string) (*FilesystemStore, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	return &FilesystemStore{Root: root}, nil
}

func (s *FilesystemStore) path(id string) string {
	// Sanitize ID to prevent path traversal
	safe := strings.ReplaceAll(id, "/", "_")
	safe = strings.ReplaceAll(safe, "..", "_")
	return filepath.Join(s.Root, safe+".json")
}

// Save serializes the conversation and writes it to disk.
func (s *FilesystemStore) Save(_ context.Context, conv *autobuild.Conversation) error {
	if conv == nil || conv.ID == "" {
		return fmt.Errorf("conversation must have an ID")
	}
	data, err := json.MarshalIndent(toSerializable(conv), "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(s.path(conv.ID), data, 0644)
}

// Load reads a conversation from disk. Returns nil if not found.
func (s *FilesystemStore) Load(_ context.Context, id string) (*autobuild.Conversation, error) {
	data, err := os.ReadFile(s.path(id))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return fromSerializable(data)
}

// List returns conversation IDs, optionally filtered by threadID.
func (s *FilesystemStore) List(_ context.Context, threadID string) ([]string, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if threadID == "" {
			ids = append(ids, id)
			continue
		}
		// Peek the file to check threadID
		data, err := os.ReadFile(filepath.Join(s.Root, e.Name()))
		if err != nil {
			continue
		}
		var sc serializableConv
		if err := json.Unmarshal(data, &sc); err == nil && sc.ThreadID == threadID {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// Delete removes a conversation file.
func (s *FilesystemStore) Delete(_ context.Context, id string) error {
	err := os.Remove(s.path(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ── Serialization ─────────────────────────────────────────────────────────────

type serializableConv struct {
	ID           string                           `json:"id"`
	ThreadID     string                           `json:"thread_id,omitempty"`
	Messages     []autobuild.ChatMessage          `json:"messages"`
	LoadedSkills map[string]autobuild.LoadedSkill `json:"loaded_skills,omitempty"`
	MemoryRead   bool                             `json:"memory_read"`
	TurnCount    int                              `json:"turn_count"`
	CreatedAt    string                           `json:"created_at"`
	LastTurnAt   string                           `json:"last_turn_at,omitempty"`
}

func toSerializable(c *autobuild.Conversation) serializableConv {
	sc := serializableConv{
		ID:           c.ID,
		ThreadID:     c.ThreadID,
		Messages:     c.Messages,
		LoadedSkills: c.LoadedSkills,
		MemoryRead:   c.MemoryRead,
		TurnCount:    c.TurnCount,
		CreatedAt:    c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if !c.LastTurnAt.IsZero() {
		sc.LastTurnAt = c.LastTurnAt.Format("2006-01-02T15:04:05Z07:00")
	}
	return sc
}

func fromSerializable(data []byte) (*autobuild.Conversation, error) {
	var sc serializableConv
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	conv := autobuild.NewConversation(sc.ID)
	conv.ThreadID = sc.ThreadID
	conv.Messages = sc.Messages
	if sc.LoadedSkills != nil {
		conv.LoadedSkills = sc.LoadedSkills
	}
	conv.MemoryRead = sc.MemoryRead
	return conv, nil
}

var _ autobuild.ConversationStore = (*FilesystemStore)(nil)
