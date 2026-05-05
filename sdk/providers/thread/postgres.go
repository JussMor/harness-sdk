package thread

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	autobuild "github.com/everfaz/autobuild-sdk"
)

// PostgresThreadProvider implements MultiUserThreadProvider against a Postgres
// database. Suitable for distributed/multi-process deployments where multiple
// backend instances share thread state.
//
// The provider accepts any *sql.DB — bring your own driver:
//
//	import _ "github.com/jackc/pgx/v5/stdlib"
//	db, _ := sql.Open("pgx", os.Getenv("DATABASE_URL"))
//	provider, _ := thread.OpenPostgres(db)
//
// Schema is created automatically. Tables: threads, thread_inbox.
//
// Differences from SQLiteThreadProvider:
//   - Uses Postgres syntax ($1, $2 placeholders)
//   - Uses TIMESTAMPTZ instead of INTEGER for timestamps
//   - Foreign keys with ON DELETE CASCADE work the same
//   - SELECT FOR UPDATE used for inbox read-and-mark atomicity
type PostgresThreadProvider struct {
	db *sql.DB
	mu sync.Mutex // serializes inbox reads (Postgres handles concurrency, but cheap insurance)
}

const postgresSchema = `
CREATE TABLE IF NOT EXISTS threads (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL DEFAULT '',
	project_id TEXT NOT NULL DEFAULT '',
	mode_id    TEXT NOT NULL DEFAULT '',
	status     TEXT NOT NULL DEFAULT 'active',
	parent_id  TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS thread_inbox (
	id              BIGSERIAL PRIMARY KEY,
	message_id      TEXT NOT NULL,
	from_thread_id  TEXT NOT NULL DEFAULT '',
	to_thread_id    TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
	content         TEXT NOT NULL,
	delivery        TEXT NOT NULL DEFAULT 'queued',
	created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	read_at         TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_thread_inbox_to ON thread_inbox(to_thread_id, created_at);
CREATE INDEX IF NOT EXISTS idx_threads_project ON threads(project_id, status);
CREATE INDEX IF NOT EXISTS idx_threads_user    ON threads(user_id, status);
`

// OpenPostgres initializes a PostgresThreadProvider using an existing *sql.DB.
// Creates the schema if it doesn't exist.
func OpenPostgres(db *sql.DB) (*PostgresThreadProvider, error) {
	if _, err := db.Exec(postgresSchema); err != nil {
		return nil, fmt.Errorf("thread/postgres: create schema: %w", err)
	}
	return &PostgresThreadProvider{db: db}, nil
}

// Create starts a thread with no user scoping.
func (p *PostgresThreadProvider) Create(ctx context.Context, projectID, modeID string) (*autobuild.Thread, error) {
	return p.createWithUser(ctx, "", projectID, modeID)
}

// CreateForUser creates a thread owned by userID.
func (p *PostgresThreadProvider) CreateForUser(ctx context.Context, userID, projectID, modeID string) (*autobuild.Thread, error) {
	return p.createWithUser(ctx, userID, projectID, modeID)
}

func (p *PostgresThreadProvider) createWithUser(ctx context.Context, userID, projectID, modeID string) (*autobuild.Thread, error) {
	t := &autobuild.Thread{
		ID:        newID("th"),
		UserID:    strings.TrimSpace(userID),
		ProjectID: strings.TrimSpace(projectID),
		ModeID:    strings.TrimSpace(modeID),
		Status:    autobuild.ThreadStatusActive,
	}
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO threads(id, user_id, project_id, mode_id, status, parent_id)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		t.ID, t.UserID, t.ProjectID, t.ModeID, string(t.Status), t.ParentID,
	)
	if err != nil {
		return nil, fmt.Errorf("thread/postgres: create: %w", err)
	}
	return t, nil
}

// Get returns thread metadata by ID.
func (p *PostgresThreadProvider) Get(ctx context.Context, threadID string) (*autobuild.Thread, error) {
	row := p.db.QueryRowContext(ctx,
		`SELECT id, user_id, project_id, mode_id, status, parent_id FROM threads WHERE id = $1`,
		threadID,
	)
	var t autobuild.Thread
	var status string
	err := row.Scan(&t.ID, &t.UserID, &t.ProjectID, &t.ModeID, &status, &t.ParentID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("thread/postgres: get %s: %w", threadID, err)
	}
	t.Status = autobuild.ThreadStatus(status)
	return &t, nil
}

// GetForUser enforces user ownership.
func (p *PostgresThreadProvider) GetForUser(ctx context.Context, userID, threadID string) (*autobuild.Thread, error) {
	t, err := p.Get(ctx, threadID)
	if err != nil || t == nil {
		return t, err
	}
	if t.UserID != userID {
		return nil, autobuild.ErrThreadAccessDenied
	}
	return t, nil
}

// Archive marks a thread as archived.
func (p *PostgresThreadProvider) Archive(ctx context.Context, threadID string) error {
	res, err := p.db.ExecContext(ctx,
		`UPDATE threads SET status = $1, updated_at = NOW() WHERE id = $2`,
		string(autobuild.ThreadStatusArchived), threadID,
	)
	if err != nil {
		return fmt.Errorf("thread/postgres: archive %s: %w", threadID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("thread/postgres: archive %s: not found", threadID)
	}
	return nil
}

// SendMessage persists a message to the recipient's inbox.
func (p *PostgresThreadProvider) SendMessage(ctx context.Context, msg autobuild.Message) error {
	if msg.ToThreadID == "" {
		return fmt.Errorf("thread/postgres: SendMessage: ToThreadID is required")
	}
	if msg.ID == "" {
		msg.ID = newID("msg")
	}
	delivery := string(msg.Delivery)
	if delivery == "" {
		delivery = string(autobuild.DeliveryQueued)
	}
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO thread_inbox(message_id, from_thread_id, to_thread_id, content, delivery)
		 VALUES ($1, $2, $3, $4, $5)`,
		msg.ID, msg.FromThreadID, msg.ToThreadID, msg.Content, delivery,
	)
	if err != nil {
		return fmt.Errorf("thread/postgres: send to %s: %w", msg.ToThreadID, err)
	}
	return nil
}

// ReadInbox returns all unread messages for threadID and marks them as read
// atomically. Uses a transaction with SELECT ... FOR UPDATE SKIP LOCKED so
// multiple concurrent readers don't deliver the same message twice.
func (p *PostgresThreadProvider) ReadInbox(ctx context.Context, threadID string) ([]autobuild.Message, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT id, message_id, from_thread_id, to_thread_id, content, delivery, created_at
		 FROM thread_inbox
		 WHERE to_thread_id = $1 AND read_at IS NULL
		 ORDER BY created_at ASC
		 FOR UPDATE SKIP LOCKED`,
		threadID,
	)
	if err != nil {
		return nil, fmt.Errorf("thread/postgres: read inbox %s: %w", threadID, err)
	}

	var msgs []autobuild.Message
	var rowIDs []int64
	for rows.Next() {
		var rowID int64
		var m autobuild.Message
		var delivery string
		var createdAt time.Time
		if err := rows.Scan(&rowID, &m.ID, &m.FromThreadID, &m.ToThreadID, &m.Content, &delivery, &createdAt); err != nil {
			rows.Close()
			return nil, err
		}
		m.Delivery = autobuild.DeliveryMode(delivery)
		m.CreatedAt = createdAt
		msgs = append(msgs, m)
		rowIDs = append(rowIDs, rowID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(rowIDs) > 0 {
		for _, id := range rowIDs {
			if _, err := tx.ExecContext(ctx,
				`UPDATE thread_inbox SET read_at = NOW() WHERE id = $1`, id,
			); err != nil {
				return nil, err
			}
		}
	}

	return msgs, tx.Commit()
}

// ListByUser returns all threads owned by userID.
func (p *PostgresThreadProvider) ListByUser(ctx context.Context, userID string, status autobuild.ThreadStatus) ([]*autobuild.Thread, error) {
	query := `SELECT id, user_id, project_id, mode_id, status, parent_id FROM threads WHERE user_id = $1`
	args := []any{userID}
	if status != "" {
		query += " AND status = $2"
		args = append(args, string(status))
	}
	query += " ORDER BY created_at DESC"
	return p.queryThreads(ctx, query, args...)
}

// ListByProject returns all threads for a project.
func (p *PostgresThreadProvider) ListByProject(ctx context.Context, projectID string, status autobuild.ThreadStatus) ([]*autobuild.Thread, error) {
	query := `SELECT id, user_id, project_id, mode_id, status, parent_id FROM threads WHERE project_id = $1`
	args := []any{projectID}
	if status != "" {
		query += " AND status = $2"
		args = append(args, string(status))
	}
	query += " ORDER BY created_at DESC"
	return p.queryThreads(ctx, query, args...)
}

// UpdateStatus changes a thread's lifecycle state.
func (p *PostgresThreadProvider) UpdateStatus(ctx context.Context, threadID string, status autobuild.ThreadStatus) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE threads SET status = $1, updated_at = NOW() WHERE id = $2`,
		string(status), threadID,
	)
	return err
}

func (p *PostgresThreadProvider) queryThreads(ctx context.Context, query string, args ...any) ([]*autobuild.Thread, error) {
	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
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

// Compile-time checks
var (
	_ autobuild.ThreadProvider          = (*PostgresThreadProvider)(nil)
	_ autobuild.MultiUserThreadProvider = (*PostgresThreadProvider)(nil)
)
