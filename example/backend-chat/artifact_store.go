package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ── Schema migration ──────────────────────────────────────────────────────────

const artifactSchema = `
CREATE TABLE IF NOT EXISTS artifacts (
  id          TEXT PRIMARY KEY,
  chat_id     INTEGER NOT NULL,
  message_id  INTEGER,
  language    TEXT NOT NULL,
  title       TEXT NOT NULL DEFAULT '',
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(chat_id) REFERENCES chats(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS artifact_versions (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  artifact_id TEXT NOT NULL,
  version     INTEGER NOT NULL,
  content     TEXT NOT NULL,
  r2_url      TEXT,
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(artifact_id) REFERENCES artifacts(id) ON DELETE CASCADE,
  UNIQUE(artifact_id, version)
);

CREATE TABLE IF NOT EXISTS artifact_storage (
  artifact_id TEXT    NOT NULL,
  key         TEXT    NOT NULL,
  value       TEXT    NOT NULL,
  shared      INTEGER NOT NULL DEFAULT 0,
  user_id     TEXT    NOT NULL DEFAULT '',
  updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(artifact_id, key, shared, user_id),
  FOREIGN KEY(artifact_id) REFERENCES artifacts(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_artifacts_chat_id
  ON artifacts(chat_id);
CREATE INDEX IF NOT EXISTS idx_artifact_versions_artifact_id
  ON artifact_versions(artifact_id, version);
`

func EnsureArtifactSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, artifactSchema)
	return err
}

// ── Domain types ──────────────────────────────────────────────────────────────

type ArtifactRecord struct {
	ID        string    `json:"id"`
	ChatID    int64     `json:"chatId"`
	MessageID *int64    `json:"messageId,omitempty"`
	Language  string    `json:"language"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`

	// Populated on read
	Versions []ArtifactVersionRecord `json:"versions,omitempty"`
	// LatestContent holds the most recent version's content when requested
	// via ListArtifactsWithLatestContent. Omitted on plain list calls.
	LatestContent string `json:"latestContent,omitempty"`
}

type ArtifactVersionRecord struct {
	ID         int64     `json:"id"`
	ArtifactID string    `json:"artifactId"`
	Version    int       `json:"version"`
	Content    string    `json:"content"`
	R2URL      string    `json:"r2Url,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

type ArtifactStorageRecord struct {
	ArtifactID string `json:"artifactId"`
	Key        string `json:"key"`
	Value      any    `json:"value"`
	Shared     bool   `json:"shared"`
	UserID     string `json:"userId,omitempty"`
}

// ── Artifact CRUD ─────────────────────────────────────────────────────────────

// CreateArtifact inserts a new artifact and its first version.
// If r2 is non-nil and content is non-empty, also uploads to R2.
func CreateArtifact(ctx context.Context, db *sql.DB, r2 *R2Client,
	chatID int64, messageID *int64, language, title, content string,
) (*ArtifactRecord, *ArtifactVersionRecord, error) {

	id := uuid.New().String()
	if title == "" {
		title = fmt.Sprintf("%s artifact", language)
	}

	_, err := db.ExecContext(ctx,
		`INSERT INTO artifacts(id, chat_id, message_id, language, title)
		 VALUES(?, ?, ?, ?, ?)`,
		id, chatID, messageID, language, title,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create artifact: %w", err)
	}

	ver, err := AddArtifactVersion(ctx, db, r2, id, 1, language, content)
	if err != nil {
		return nil, nil, err
	}

	art := &ArtifactRecord{
		ID:        id,
		ChatID:    chatID,
		MessageID: messageID,
		Language:  language,
		Title:     title,
		CreatedAt: time.Now(),
	}
	return art, ver, nil
}

// AddArtifactVersion adds a new version to an existing artifact.
func AddArtifactVersion(ctx context.Context, db *sql.DB, r2 *R2Client,
	artifactID string, version int, language, content string,
) (*ArtifactVersionRecord, error) {

	var r2URL string
	if r2.IsAvailable() && content != "" {
		key := ArtifactKey(artifactID, version, "content."+langExt(language))
		url, err := r2.Put(ctx, key, contentTypeFor(language), []byte(content))
		if err == nil {
			r2URL = url
		}
		// R2 upload failure is non-fatal — content is always in SQLite too
	}

	res, err := db.ExecContext(ctx,
		`INSERT INTO artifact_versions(artifact_id, version, content, r2_url)
		 VALUES(?, ?, ?, ?)`,
		artifactID, version, content, r2URL,
	)
	if err != nil {
		return nil, fmt.Errorf("add artifact version: %w", err)
	}

	id, _ := res.LastInsertId()
	return &ArtifactVersionRecord{
		ID:         id,
		ArtifactID: artifactID,
		Version:    version,
		Content:    content,
		R2URL:      r2URL,
		CreatedAt:  time.Now(),
	}, nil
}

// GetArtifact returns an artifact with all its versions.
func GetArtifact(ctx context.Context, db *sql.DB, id string) (*ArtifactRecord, error) {
	var art ArtifactRecord
	var msgID sql.NullInt64
	err := db.QueryRowContext(ctx,
		`SELECT id, chat_id, message_id, language, title, created_at
		 FROM artifacts WHERE id = ?`, id,
	).Scan(&art.ID, &art.ChatID, &msgID, &art.Language, &art.Title, &art.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get artifact: %w", err)
	}
	if msgID.Valid {
		art.MessageID = &msgID.Int64
	}

	versions, err := listArtifactVersions(ctx, db, id)
	if err != nil {
		return nil, err
	}
	art.Versions = versions
	return &art, nil
}

// ListArtifactsForChat returns all artifacts for a chat (without versions).
func ListArtifactsForChat(ctx context.Context, db *sql.DB, chatID int64) ([]ArtifactRecord, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, chat_id, message_id, language, title, created_at
		 FROM artifacts WHERE chat_id = ? ORDER BY created_at ASC`, chatID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var arts []ArtifactRecord
	for rows.Next() {
		var a ArtifactRecord
		var msgID sql.NullInt64
		if err := rows.Scan(&a.ID, &a.ChatID, &msgID, &a.Language, &a.Title, &a.CreatedAt); err != nil {
			return nil, err
		}
		if msgID.Valid {
			a.MessageID = &msgID.Int64
		}
		arts = append(arts, a)
	}
	return arts, rows.Err()
}

// ListArtifactsWithLatestContent returns all artifacts for a chat, each
// populated with the content of its most recent version. This is used when
// restoring the artifact list after a page reload.
func ListArtifactsWithLatestContent(ctx context.Context, db *sql.DB, chatID int64) ([]ArtifactRecord, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT a.id, a.chat_id, a.message_id, a.language, a.title, a.created_at,
		       COALESCE(av.content, '')
		FROM artifacts a
		LEFT JOIN artifact_versions av
		  ON av.artifact_id = a.id
		  AND av.version = (
		        SELECT MAX(v2.version)
		        FROM artifact_versions v2
		        WHERE v2.artifact_id = a.id
		      )
		WHERE a.chat_id = ?
		ORDER BY a.created_at ASC`, chatID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var arts []ArtifactRecord
	for rows.Next() {
		var a ArtifactRecord
		var msgID sql.NullInt64
		if err := rows.Scan(&a.ID, &a.ChatID, &msgID, &a.Language, &a.Title, &a.CreatedAt, &a.LatestContent); err != nil {
			return nil, err
		}
		if msgID.Valid {
			a.MessageID = &msgID.Int64
		}
		arts = append(arts, a)
	}
	return arts, rows.Err()
}

// NextVersion returns the next version number for an artifact.
func NextVersion(ctx context.Context, db *sql.DB, artifactID string) (int, error) {
	var max sql.NullInt64
	err := db.QueryRowContext(ctx,
		`SELECT MAX(version) FROM artifact_versions WHERE artifact_id = ?`,
		artifactID,
	).Scan(&max)
	if err != nil {
		return 0, err
	}
	if max.Valid {
		return int(max.Int64) + 1, nil
	}
	return 1, nil
}

func listArtifactVersions(ctx context.Context, db *sql.DB, artifactID string) ([]ArtifactVersionRecord, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, artifact_id, version, content, COALESCE(r2_url,''), created_at
		 FROM artifact_versions WHERE artifact_id = ? ORDER BY version ASC`,
		artifactID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []ArtifactVersionRecord
	for rows.Next() {
		var v ArtifactVersionRecord
		if err := rows.Scan(&v.ID, &v.ArtifactID, &v.Version, &v.Content, &v.R2URL, &v.CreatedAt); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

// ── Artifact storage (key-value, personal or shared) ─────────────────────────

// GetArtifactStorage returns all key-value pairs for an artifact.
// shared=true → shared storage; shared=false → personal storage (scoped to userID).
func GetArtifactStorage(ctx context.Context, db *sql.DB, artifactID string, shared bool, userID string) (map[string]any, error) {
	uid := ""
	if !shared {
		uid = userID
	}
	rows, err := db.QueryContext(ctx,
		`SELECT key, value FROM artifact_storage
		 WHERE artifact_id = ? AND shared = ? AND user_id = ?`,
		artifactID, boolToInt(shared), uid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[string]any{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		var parsed any
		if json.Unmarshal([]byte(v), &parsed) == nil {
			result[k] = parsed
		} else {
			result[k] = v
		}
	}
	return result, rows.Err()
}

// SetArtifactStorage upserts a key-value pair for an artifact.
// value must be JSON-serializable. Text-only (no binary, per Anthropic spec).
func SetArtifactStorage(ctx context.Context, db *sql.DB,
	artifactID, key string, value any, shared bool, userID string,
) error {
	uid := ""
	if !shared {
		uid = userID
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal storage value: %w", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO artifact_storage(artifact_id, key, value, shared, user_id, updated_at)
		VALUES(?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(artifact_id, key, shared, user_id)
		DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		artifactID, key, string(raw), boolToInt(shared), uid,
	)
	return err
}

// DeleteArtifactStorageKey deletes a single key from artifact storage.
func DeleteArtifactStorageKey(ctx context.Context, db *sql.DB, artifactID, key string, shared bool, userID string) error {
	uid := ""
	if !shared {
		uid = userID
	}
	_, err := db.ExecContext(ctx,
		`DELETE FROM artifact_storage WHERE artifact_id = ? AND key = ? AND shared = ? AND user_id = ?`,
		artifactID, key, boolToInt(shared), uid,
	)
	return err
}

// ── helpers ───────────────────────────────────────────────────────────────────

func langExt(language string) string {
	switch strings.ToLower(language) {
	case "markdown", "md":
		return "md"
	case "html", "htm":
		return "html"
	case "jsx":
		return "jsx"
	case "tsx":
		return "tsx"
	case "svg":
		return "svg"
	case "css":
		return "css"
	case "python", "py":
		return "py"
	case "go":
		return "go"
	case "typescript", "ts":
		return "ts"
	case "javascript", "js":
		return "js"
	default:
		return "txt"
	}
}
