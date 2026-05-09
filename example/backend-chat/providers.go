package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func OpenSQLite(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}

	// Reduce "database is locked" errors under concurrent reads/writes.
	if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000;`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON;`); err != nil {
		return nil, err
	}

	return db, nil
}

func EnsureSchema(ctx context.Context, db *sql.DB) error {
	schema := `
CREATE TABLE IF NOT EXISTS chats (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  title TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  chat_id INTEGER NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  model TEXT NOT NULL,
  metadata TEXT NOT NULL DEFAULT '{}',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS chat_sandbox_bindings (
	chat_id INTEGER PRIMARY KEY,
	sandbox_id TEXT NOT NULL,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY(chat_id) REFERENCES chats(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_messages_chat_id ON messages(chat_id, id);
`
	_, err := db.ExecContext(ctx, schema)
	if err != nil {
		return err
	}
	// Migrate: add metadata column if missing (for existing databases)
	_, _ = db.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`)
	return nil
}

func CreateChat(ctx context.Context, db *sql.DB, title string) (Chat, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO chats(title) VALUES(?)`, title)
	if err != nil {
		return Chat{}, err
	}
	id, _ := res.LastInsertId()
	return getChat(ctx, db, id)
}

func ListChats(ctx context.Context, db *sql.DB) ([]Chat, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, title, created_at, updated_at FROM chats ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]Chat, 0)
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.ID, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

func InsertMessage(ctx context.Context, db *sql.DB, chatID int64, role, content, model string, opts ...MessageMetadata) (Message, error) {
	if role == "" {
		role = "user"
	}
	if model == "" {
		model = "unknown"
	}
	metadata := "{}"
	if len(opts) > 0 {
		if raw, err := json.Marshal(opts[0]); err == nil {
			metadata = string(raw)
		}
	}
	res, err := db.ExecContext(ctx, `INSERT INTO messages(chat_id, role, content, model, metadata) VALUES(?,?,?,?,?)`, chatID, role, content, model, metadata)
	if err != nil {
		return Message{}, err
	}
	_, _ = db.ExecContext(ctx, `UPDATE chats SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, chatID)
	id, _ := res.LastInsertId()
	return getMessage(ctx, db, id)
}

func UpdateMessageMetadata(ctx context.Context, db *sql.DB, msgID int64, meta MessageMetadata) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE messages SET metadata = ? WHERE id = ?`, string(raw), msgID)
	return err
}

func ListMessages(ctx context.Context, db *sql.DB, chatID int64) ([]Message, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, chat_id, role, content, model, COALESCE(metadata, '{}'), created_at FROM messages WHERE chat_id = ? ORDER BY id ASC`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]Message, 0)
	for rows.Next() {
		var m Message
		var metaRaw string
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Role, &m.Content, &m.Model, &metaRaw, &m.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(metaRaw), &m.Metadata)
		items = append(items, m)
	}
	return items, rows.Err()
}

func getChat(ctx context.Context, db *sql.DB, id int64) (Chat, error) {
	var c Chat
	err := db.QueryRowContext(ctx, `SELECT id, title, created_at, updated_at FROM chats WHERE id = ?`, id).
		Scan(&c.ID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func getMessage(ctx context.Context, db *sql.DB, id int64) (Message, error) {
	var m Message
	var metaRaw string
	err := db.QueryRowContext(ctx, `SELECT id, chat_id, role, content, model, COALESCE(metadata, '{}'), created_at FROM messages WHERE id = ?`, id).
		Scan(&m.ID, &m.ChatID, &m.Role, &m.Content, &m.Model, &metaRaw, &m.CreatedAt)
	_ = json.Unmarshal([]byte(metaRaw), &m.Metadata)
	return m, err
}

func GetChatSandboxBinding(ctx context.Context, db *sql.DB, chatID int64) (string, error) {
	var sandboxID string
	err := db.QueryRowContext(ctx, `SELECT sandbox_id FROM chat_sandbox_bindings WHERE chat_id = ?`, chatID).Scan(&sandboxID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return sandboxID, nil
}

func UpsertChatSandboxBinding(ctx context.Context, db *sql.DB, chatID int64, sandboxID string) error {
	if _, err := db.ExecContext(ctx, `
		INSERT INTO chat_sandbox_bindings(chat_id, sandbox_id, updated_at)
		VALUES(?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(chat_id)
		DO UPDATE SET sandbox_id = excluded.sandbox_id, updated_at = CURRENT_TIMESTAMP
	`, chatID, sandboxID); err != nil {
		return err
	}
	return nil
}

func DeleteChatSandboxBinding(ctx context.Context, db *sql.DB, chatID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM chat_sandbox_bindings WHERE chat_id = ?`, chatID)
	return err
}

type CentrifugoClient struct {
	apiURL string
	apiKey string
	http   *http.Client
}

func NewCentrifugoClient(apiURL, apiKey string) *CentrifugoClient {
	return &CentrifugoClient{
		apiURL: apiURL,
		apiKey: apiKey,
		http:   &http.Client{Timeout: 3 * time.Second},
	}
}

func (c *CentrifugoClient) PublishChatMessage(ctx context.Context, chatID int64, msg Message) error {
	if c.apiURL == "" || c.apiKey == "" {
		return nil
	}
	payload := map[string]any{
		"method": "publish",
		"params": map[string]any{
			"channel": fmt.Sprintf("chat:%d", chatID),
			"data": map[string]any{
				"type":    "message.created",
				"message": msg,
			},
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "apikey "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("centrifugo publish failed: %s", resp.Status)
	}
	return nil
}
