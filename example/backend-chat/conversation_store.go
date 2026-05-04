package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	ab "github.com/everfaz/autobuild-sdk"
)

// SQLiteConversationStore persists SDK Conversations in the existing SQLite DB.
// Uses a separate `sdk_conversations` table alongside the existing chats/messages.
type SQLiteConversationStore struct {
	db *sql.DB
}

func NewSQLiteConversationStore(db *sql.DB) *SQLiteConversationStore {
	return &SQLiteConversationStore{db: db}
}

// EnsureConversationSchema creates the sdk_conversations table if missing.
func EnsureConversationSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS sdk_conversations (
  id TEXT PRIMARY KEY,
  thread_id TEXT,
  messages_json TEXT NOT NULL DEFAULT '[]',
  loaded_skills_json TEXT NOT NULL DEFAULT '{}',
  memory_read INTEGER NOT NULL DEFAULT 0,
  turn_count INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_sdk_conv_thread ON sdk_conversations(thread_id);
`)
	return err
}

func (s *SQLiteConversationStore) Save(ctx context.Context, conv *ab.Conversation) error {
	if conv == nil || conv.ID == "" {
		return fmt.Errorf("conversation must have an ID")
	}

	messagesJSON, err := json.Marshal(conv.Messages)
	if err != nil {
		return fmt.Errorf("marshal messages: %w", err)
	}
	skillsJSON, err := json.Marshal(conv.LoadedSkills)
	if err != nil {
		return fmt.Errorf("marshal skills: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO sdk_conversations(id, thread_id, messages_json, loaded_skills_json, memory_read, turn_count, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  thread_id = excluded.thread_id,
  messages_json = excluded.messages_json,
  loaded_skills_json = excluded.loaded_skills_json,
  memory_read = excluded.memory_read,
  turn_count = excluded.turn_count,
  updated_at = excluded.updated_at`,
		conv.ID,
		conv.ThreadID,
		string(messagesJSON),
		string(skillsJSON),
		boolToInt(conv.MemoryRead),
		conv.TurnCount,
		conv.CreatedAt.Format(time.RFC3339),
		time.Now().Format(time.RFC3339),
	)
	return err
}

func (s *SQLiteConversationStore) Load(ctx context.Context, id string) (*ab.Conversation, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, thread_id, messages_json, loaded_skills_json, memory_read, turn_count, created_at
FROM sdk_conversations WHERE id = ?`, id)

	var (
		convID       string
		threadID     sql.NullString
		messagesJSON string
		skillsJSON   string
		memoryRead   int
		turnCount    int
		createdAt    string
	)
	err := row.Scan(&convID, &threadID, &messagesJSON, &skillsJSON, &memoryRead, &turnCount, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan conversation: %w", err)
	}

	conv := ab.NewConversation(convID)
	conv.ThreadID = threadID.String
	conv.MemoryRead = memoryRead == 1

	if err := json.Unmarshal([]byte(messagesJSON), &conv.Messages); err != nil {
		return nil, fmt.Errorf("unmarshal messages: %w", err)
	}
	if err := json.Unmarshal([]byte(skillsJSON), &conv.LoadedSkills); err != nil {
		conv.LoadedSkills = make(map[string]ab.LoadedSkill)
	}
	if conv.Messages == nil {
		conv.Messages = make([]ab.ChatMessage, 0)
	}
	return conv, nil
}

func (s *SQLiteConversationStore) List(ctx context.Context, threadID string) ([]string, error) {
	var rows *sql.Rows
	var err error
	if threadID == "" {
		rows, err = s.db.QueryContext(ctx, `SELECT id FROM sdk_conversations ORDER BY updated_at DESC`)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT id FROM sdk_conversations WHERE thread_id = ? ORDER BY updated_at DESC`, threadID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func (s *SQLiteConversationStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sdk_conversations WHERE id = ?`, id)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ConversationID builds a stable conversation ID from a chat ID.
// This links the SDK Conversation to the backend's chat entity.
func ConversationID(chatID int64) string {
	return fmt.Sprintf("chat-%d", chatID)
}

// LoadOrCreateConversation loads a persisted SDK Conversation for a chat,
// or creates a fresh one and pre-populates it from the DB message history.
func LoadOrCreateConversation(ctx context.Context, store ab.ConversationStore, chatID int64, history []Message) (*ab.Conversation, error) {
	id := ConversationID(chatID)

	if store != nil {
		conv, err := store.Load(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("load conversation: %w", err)
		}
		if conv != nil {
			return conv, nil
		}
	}

	// First time — build conversation from DB history
	conv := ab.NewConversation(id)
	// Pre-populate all but last user message (Runtime.Run adds that)
	msgs := toAgentMessages(history)
	if len(msgs) > 0 {
		for _, m := range msgs[:len(msgs)-1] {
			switch m.Role {
			case ab.RoleUser:
				conv.AppendUser(m.Content)
			case ab.RoleAssistant:
				conv.AppendAssistant(m.Content)
			}
		}
	}
	return conv, nil
}

// toAgentMessages converts DB messages to SDK ChatMessages.
func toAgentMessages(messages []Message) []ab.ChatMessage {
	out := make([]ab.ChatMessage, 0, len(messages))
	for _, msg := range messages {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		role := ab.RoleUser
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "assistant":
			role = ab.RoleAssistant
		case "tool":
			role = ab.RoleTool
		case "system":
			role = ab.RoleSystem
		}
		out = append(out, ab.ChatMessage{Role: role, Content: content})
	}
	return out
}

// abToMessages converts []ab.ChatMessage → []Message for LoadOrCreateConversation.
// Uses dummy IDs since we only need role+content.
func abToMessages(msgs []ab.ChatMessage) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, Message{
			Role:    string(m.Role),
			Content: m.Content,
		})
	}
	return out
}

// messagesFromAB is an alias for abToMessages.
func messagesFromAB(msgs []ab.ChatMessage) []Message {
	return abToMessages(msgs)
}
