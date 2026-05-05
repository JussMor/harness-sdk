package thread

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	autobuild "github.com/everfaz/autobuild-sdk"
	_ "github.com/mattn/go-sqlite3"
)

// SQLiteThreadProvider implements autobuild.ThreadProvider using SQLite.
// Suitable for single-process deployments (backend-chat, local dev, edge).
// For multi-process or distributed deployments, use a Postgres-backed provider.
//
// Schema is created automatically on Open().
//
//	db, _ := sql.Open("sqlite3", "threads.db")
//	provider, _ := thread.OpenSQLite(db)
//	engine.Threads = provider
type SQLiteThreadProvider struct {
	db *sql.DB
	mu sync.Mutex // protects inbox delivery
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS threads (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL DEFAULT '',
	project_id TEXT NOT NULL DEFAULT '',
	mode_id    TEXT NOT NULL DEFAULT '',
	status     TEXT NOT NULL DEFAULT 'active',
	parent_id  TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS thread_inbox (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	message_id      TEXT    NOT NULL,
	from_thread_id  TEXT    NOT NULL DEFAULT '',
	to_thread_id    TEXT    NOT NULL,
	content         TEXT    NOT NULL,
	delivery        TEXT    NOT NULL DEFAULT 'queued',
	created_at      INTEGER NOT NULL,
	read_at         INTEGER,
	FOREIGN KEY(to_thread_id) REFERENCES threads(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_thread_inbox_to     ON thread_inbox(to_thread_id, created_at);
CREATE INDEX IF NOT EXISTS idx_threads_project     ON threads(project_id, status);
CREATE INDEX IF NOT EXISTS idx_threads_user        ON threads(user_id, status);
`

// OpenSQLite initializes a SQLiteThreadProvider using an existing *sql.DB.
// Creates the schema if it doesn't exist.
func OpenSQLite(db *sql.DB) (*SQLiteThreadProvider, error) {
	if _, err := db.Exec(sqliteSchema); err != nil {
		return nil, fmt.Errorf("thread: create schema: %w", err)
	}
	return &SQLiteThreadProvider{db: db}, nil
}

// OpenSQLiteFile opens (or creates) a SQLite database at path and returns
// a ready-to-use SQLiteThreadProvider.
func OpenSQLiteFile(path string) (*SQLiteThreadProvider, error) {
	db, err := sql.Open("sqlite3", path+"?_journal=WAL&_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("thread: open db %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // SQLite WAL supports one writer
	return OpenSQLite(db)
}

// Create inserts a new active thread (single-user, no UserID) and returns it.
func (p *SQLiteThreadProvider) Create(_ context.Context, projectID, modeID string) (*autobuild.Thread, error) {
	return p.createWithUser("", projectID, modeID)
}

// CreateForUser creates a thread owned by userID.
func (p *SQLiteThreadProvider) CreateForUser(_ context.Context, userID, projectID, modeID string) (*autobuild.Thread, error) {
	return p.createWithUser(userID, projectID, modeID)
}

func (p *SQLiteThreadProvider) createWithUser(userID, projectID, modeID string) (*autobuild.Thread, error) {
	t := &autobuild.Thread{
		ID:        newID("th"),
		UserID:    strings.TrimSpace(userID),
		ProjectID: strings.TrimSpace(projectID),
		ModeID:    strings.TrimSpace(modeID),
		Status:    autobuild.ThreadStatusActive,
	}
	now := time.Now().UnixNano()
	_, err := p.db.Exec(
		`INSERT INTO threads(id, user_id, project_id, mode_id, status, parent_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.ProjectID, t.ModeID, string(t.Status), t.ParentID, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("thread: create: %w", err)
	}
	return t, nil
}

// Get returns thread metadata by ID. Returns nil, nil if not found.
func (p *SQLiteThreadProvider) Get(_ context.Context, threadID string) (*autobuild.Thread, error) {
	row := p.db.QueryRow(
		`SELECT id, user_id, project_id, mode_id, status, parent_id FROM threads WHERE id = ?`,
		threadID,
	)
	var t autobuild.Thread
	var status string
	err := row.Scan(&t.ID, &t.UserID, &t.ProjectID, &t.ModeID, &status, &t.ParentID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("thread: get %s: %w", threadID, err)
	}
	t.Status = autobuild.ThreadStatus(status)
	return &t, nil
}

// GetForUser returns a thread only if it belongs to userID.
// Returns ErrThreadAccessDenied if the thread exists but belongs to another user.
func (p *SQLiteThreadProvider) GetForUser(ctx context.Context, userID, threadID string) (*autobuild.Thread, error) {
	t, err := p.Get(ctx, threadID)
	if err != nil || t == nil {
		return t, err
	}
	if t.UserID != userID {
		return nil, autobuild.ErrThreadAccessDenied
	}
	return t, nil
}

// ListByUser returns all threads owned by userID, optionally filtered by status.
// Newest first.
func (p *SQLiteThreadProvider) ListByUser(ctx context.Context, userID string, status autobuild.ThreadStatus) ([]*autobuild.Thread, error) {
	query := `SELECT id, user_id, project_id, mode_id, status, parent_id FROM threads WHERE user_id = ?`
	args := []any{userID}
	if status != "" {
		query += " AND status = ?"
		args = append(args, string(status))
	}
	query += " ORDER BY created_at DESC"

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("thread: list user %s: %w", userID, err)
	}
	defer rows.Close()

	var threads []*autobuild.Thread
	for rows.Next() {
		var t autobuild.Thread
		var s string
		if err := rows.Scan(&t.ID, &t.UserID, &t.ProjectID, &t.ModeID, &s, &t.ParentID); err != nil {
			return nil, err
		}
		t.Status = autobuild.ThreadStatus(s)
		threads = append(threads, &t)
	}
	return threads, rows.Err()
}

// Archive marks a thread as archived.
func (p *SQLiteThreadProvider) Archive(_ context.Context, threadID string) error {
	now := time.Now().UnixNano()
	res, err := p.db.Exec(
		`UPDATE threads SET status = ?, updated_at = ? WHERE id = ?`,
		string(autobuild.ThreadStatusArchived), now, threadID,
	)
	if err != nil {
		return fmt.Errorf("thread: archive %s: %w", threadID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("thread: archive %s: not found", threadID)
	}
	return nil
}

// SendMessage delivers a message to another thread's inbox.
// The message can be read via ReadInbox.
func (p *SQLiteThreadProvider) SendMessage(_ context.Context, msg autobuild.Message) error {
	if msg.ToThreadID == "" {
		return fmt.Errorf("thread: SendMessage: ToThreadID is required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now().UnixNano()
	if msg.ID == "" {
		msg.ID = newID("msg")
	}
	delivery := string(msg.Delivery)
	if delivery == "" {
		delivery = string(autobuild.DeliveryQueued)
	}

	_, err := p.db.Exec(
		`INSERT INTO thread_inbox(message_id, from_thread_id, to_thread_id, content, delivery, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.FromThreadID, msg.ToThreadID, msg.Content, delivery, now,
	)
	if err != nil {
		return fmt.Errorf("thread: send message to %s: %w", msg.ToThreadID, err)
	}
	return nil
}

// ReadInbox returns all unread messages for threadID, ordered oldest first.
// Marks them as read.
func (p *SQLiteThreadProvider) ReadInbox(ctx context.Context, threadID string) ([]autobuild.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	rows, err := p.db.QueryContext(ctx,
		`SELECT id, message_id, from_thread_id, to_thread_id, content, delivery, created_at
		 FROM thread_inbox
		 WHERE to_thread_id = ? AND read_at IS NULL ORDER BY created_at ASC`,
		threadID,
	)
	if err != nil {
		return nil, fmt.Errorf("thread: read inbox %s: %w", threadID, err)
	}
	defer rows.Close()

	var msgs []autobuild.Message
	var rowIDs []int64
	for rows.Next() {
		var rowID int64
		var m autobuild.Message
		var delivery string
		var createdAtNano int64
		if err := rows.Scan(&rowID, &m.ID, &m.FromThreadID, &m.ToThreadID, &m.Content, &delivery, &createdAtNano); err != nil {
			return nil, err
		}
		m.Delivery = autobuild.DeliveryMode(delivery)
		m.CreatedAt = time.Unix(0, createdAtNano)
		msgs = append(msgs, m)
		rowIDs = append(rowIDs, rowID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Mark as read
	if len(rowIDs) > 0 {
		now := time.Now().UnixNano()
		for _, id := range rowIDs {
			_, _ = p.db.ExecContext(ctx,
				`UPDATE thread_inbox SET read_at = ? WHERE id = ?`, now, id,
			)
		}
	}
	return msgs, nil
}

// ListByProject returns all threads for a project, newest first.
// status="" returns threads in any status.
func (p *SQLiteThreadProvider) ListByProject(ctx context.Context, projectID string, status autobuild.ThreadStatus) ([]*autobuild.Thread, error) {
	query := `SELECT id, user_id, project_id, mode_id, status, parent_id FROM threads WHERE project_id = ?`
	args := []any{projectID}
	if status != "" {
		query += " AND status = ?"
		args = append(args, string(status))
	}
	query += " ORDER BY created_at DESC"

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("thread: list project %s: %w", projectID, err)
	}
	defer rows.Close()

	var threads []*autobuild.Thread
	for rows.Next() {
		var t autobuild.Thread
		var s string
		if err := rows.Scan(&t.ID, &t.UserID, &t.ProjectID, &t.ModeID, &s, &t.ParentID); err != nil {
			return nil, err
		}
		t.Status = autobuild.ThreadStatus(s)
		threads = append(threads, &t)
	}
	return threads, rows.Err()
}

// UpdateStatus changes a thread's status.
func (p *SQLiteThreadProvider) UpdateStatus(_ context.Context, threadID string, status autobuild.ThreadStatus) error {
	now := time.Now().UnixNano()
	_, err := p.db.Exec(
		`UPDATE threads SET status = ?, updated_at = ? WHERE id = ?`,
		string(status), now, threadID,
	)
	return err
}

// Close closes the underlying database. Only call this if OpenSQLiteFile was used.
func (p *SQLiteThreadProvider) Close() error {
	return p.db.Close()
}

// Compile-time checks
var (
	_ autobuild.ThreadProvider          = (*SQLiteThreadProvider)(nil)
	_ autobuild.MultiUserThreadProvider = (*SQLiteThreadProvider)(nil)
)
