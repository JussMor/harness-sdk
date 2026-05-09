package autobuild

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// ConversationStore persists Conversations across process restarts.
// Implementations might use SQLite, Postgres, Redis, or local files.
//
// The Runtime uses ConversationStore to load/save state automatically when
// configured. Without a store, conversations live only in memory and are
// lost when the process exits.
type ConversationStore interface {
	// Save persists the conversation. Overwrites by ID.
	Save(ctx context.Context, conv *Conversation) error

	// Load retrieves a conversation by ID. Returns nil if not found.
	Load(ctx context.Context, id string) (*Conversation, error)

	// List returns IDs of stored conversations, optionally filtered by ThreadID.
	// Pass empty threadID to list all.
	List(ctx context.Context, threadID string) ([]string, error)

	// Delete removes a conversation.
	Delete(ctx context.Context, id string) error
}

// ── In-memory implementation ─────────────────────────────────────────────────

// InMemoryConversationStore is a process-local store. Suitable for tests
// and single-process apps. Replace with a persistent store for production.
type InMemoryConversationStore struct {
	mu    sync.RWMutex
	convs map[string][]byte // ID → serialized JSON
}

// NewInMemoryConversationStore returns an empty in-memory store.
func NewInMemoryConversationStore() *InMemoryConversationStore {
	return &InMemoryConversationStore{
		convs: make(map[string][]byte),
	}
}

// Save serializes and stores the conversation.
func (s *InMemoryConversationStore) Save(_ context.Context, conv *Conversation) error {
	if conv == nil || conv.ID == "" {
		return fmt.Errorf("conversation must have an ID")
	}
	data, err := serializeConversation(conv)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.convs[conv.ID] = data
	return nil
}

// Load deserializes a conversation by ID. Returns nil if not found.
func (s *InMemoryConversationStore) Load(_ context.Context, id string) (*Conversation, error) {
	s.mu.RLock()
	data, ok := s.convs[id]
	s.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	return deserializeConversation(data)
}

// List returns all stored conversation IDs (filtered by threadID if non-empty).
func (s *InMemoryConversationStore) List(_ context.Context, threadID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var ids []string
	for id, data := range s.convs {
		if threadID == "" {
			ids = append(ids, id)
			continue
		}
		// Peek at the thread ID without full deserialization
		conv, err := deserializeConversation(data)
		if err == nil && conv.ThreadID == threadID {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// Delete removes a conversation by ID.
func (s *InMemoryConversationStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.convs, id)
	return nil
}

// ── Serialization ────────────────────────────────────────────────────────────

// serializableConversation is the JSON-friendly view of a Conversation
// (mu omitted, maps as plain).
type serializableConversation struct {
	ID         string        `json:"id"`
	ThreadID   string        `json:"thread_id,omitempty"`
	Messages   []ChatMessage `json:"messages"`
	MemoryRead bool          `json:"memory_read"`
	TurnCount  int           `json:"turn_count"`
	CreatedAt  string        `json:"created_at"`
	LastTurnAt string        `json:"last_turn_at,omitempty"`
}

func serializeConversation(c *Conversation) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sc := serializableConversation{
		ID:         c.ID,
		ThreadID:   c.ThreadID,
		Messages:   c.Messages,
		MemoryRead: c.MemoryRead,
		TurnCount:  c.TurnCount,
		CreatedAt:  c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if !c.LastTurnAt.IsZero() {
		sc.LastTurnAt = c.LastTurnAt.Format("2006-01-02T15:04:05Z07:00")
	}
	return json.Marshal(sc)
}

func deserializeConversation(data []byte) (*Conversation, error) {
	var sc serializableConversation
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, fmt.Errorf("deserialize conversation: %w", err)
	}
	c := &Conversation{
		ID:         sc.ID,
		ThreadID:   sc.ThreadID,
		Messages:   sc.Messages,
		MemoryRead: sc.MemoryRead,
		TurnCount:  sc.TurnCount,
	}
	if c.Messages == nil {
		c.Messages = make([]ChatMessage, 0)
	}
	return c, nil
}
