package localdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	mailpkg "github.com/MiyuLabs/mail/internal/mail"
	_ "modernc.org/sqlite"
)

// Client wraps the local SQLite database connection.
type Client struct {
	db *sql.DB
}

// OpenOrCreate opens the local SQLite database, creating it and applying
// the schema if it doesn't exist.
func OpenOrCreate(dbPath string) (*Client, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("localdb: failed to create directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("localdb: failed to open db: %w", err)
	}

	c := &Client{db: db}
	if err := c.initSchema(); err != nil {
		db.Close()
		return nil, err
	}

	return c, nil
}

// Close closes the database.
func (c *Client) Close() error {
	return c.db.Close()
}

// initSchema creates the necessary tables.
func (c *Client) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS identities (
		id            TEXT PRIMARY KEY,
		address       TEXT NOT NULL UNIQUE,
		display_name  TEXT NOT NULL,
		identity_type TEXT NOT NULL DEFAULT 'mailbox',
		is_active     INTEGER DEFAULT 1,
		created_at    TEXT NOT NULL,
		updated_at    TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS threads (
		id              TEXT PRIMARY KEY,
		subject         TEXT NOT NULL,
		identity_id     TEXT NOT NULL,
		last_message_at TEXT NOT NULL,
		message_count   INTEGER DEFAULT 0,
		is_read         INTEGER DEFAULT 0,
		is_starred      INTEGER DEFAULT 0,
		is_archived     INTEGER DEFAULT 0,
		is_trashed      INTEGER DEFAULT 0,
		snippet         TEXT,
		participant_names TEXT,
		created_at      TEXT NOT NULL,
		updated_at      TEXT NOT NULL,
		FOREIGN KEY (identity_id) REFERENCES identities(id)
	);

	CREATE TABLE IF NOT EXISTS messages (
		id              TEXT PRIMARY KEY,
		thread_id       TEXT NOT NULL,
		message_id      TEXT NOT NULL UNIQUE,
		in_reply_to     TEXT,
		"references"    TEXT,
		from_address    TEXT NOT NULL,
		from_name       TEXT,
		to_addresses    TEXT NOT NULL,
		cc_addresses    TEXT,
		bcc_addresses   TEXT,
		reply_to        TEXT,
		subject         TEXT NOT NULL,
		body_text       TEXT,
		body_html       TEXT,
		snippet         TEXT,
		original_to     TEXT,
		direction       TEXT NOT NULL DEFAULT 'inbound',
		imap_uid        INTEGER,
		resend_id       TEXT,
		is_read         INTEGER DEFAULT 0,
		is_draft        INTEGER DEFAULT 0,
		has_attachments INTEGER DEFAULT 0,
		received_at     TEXT NOT NULL,
		created_at      TEXT NOT NULL,
		FOREIGN KEY (thread_id) REFERENCES threads(id)
	);

	CREATE TABLE IF NOT EXISTS participants (
		id            TEXT PRIMARY KEY,
		address       TEXT NOT NULL UNIQUE,
		display_name  TEXT,
		last_seen_at  TEXT NOT NULL,
		message_count INTEGER DEFAULT 1
	);
	CREATE INDEX IF NOT EXISTS idx_participants_address ON participants(address);

	CREATE TABLE IF NOT EXISTS outbox (
		id            TEXT PRIMARY KEY,
		message_id    TEXT NOT NULL,
		identity_id   TEXT NOT NULL,
		payload       TEXT NOT NULL,
		status        TEXT NOT NULL DEFAULT 'pending',
		retry_count   INTEGER DEFAULT 0,
		last_error    TEXT,
		next_retry_at TEXT NOT NULL,
		created_at    TEXT NOT NULL,
		updated_at    TEXT NOT NULL
	);
	`
	_, err := c.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("localdb: failed to initialize schema: %w", err)
	}
	return nil
}

// UpsertIdentity inserts or updates an identity.
func (c *Client) UpsertIdentity(ctx context.Context, id *mailpkg.Identity) error {
	query := `
		INSERT INTO identities (id, address, display_name, identity_type, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			display_name = excluded.display_name,
			identity_type = excluded.identity_type,
			is_active = excluded.is_active,
			updated_at = excluded.updated_at
	`
	isActive := 0
	if id.IsActive {
		isActive = 1
	}
	_, err := c.db.ExecContext(ctx, query,
		id.ID, id.Address, id.DisplayName, id.Type, isActive,
		id.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		id.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	)
	return err
}

// UpsertThread inserts or updates a thread.
func (c *Client) UpsertThread(ctx context.Context, t *mailpkg.Thread) error {
	query := `
		INSERT INTO threads (
			id, subject, identity_id, last_message_at, message_count,
			is_read, is_starred, is_archived, is_trashed, snippet,
			participant_names, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			last_message_at = excluded.last_message_at,
			message_count = excluded.message_count,
			is_read = excluded.is_read,
			is_starred = excluded.is_starred,
			is_archived = excluded.is_archived,
			is_trashed = excluded.is_trashed,
			snippet = excluded.snippet,
			participant_names = excluded.participant_names,
			updated_at = excluded.updated_at
	`
	boolToInt := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	_, err := c.db.ExecContext(ctx, query,
		t.ID, t.Subject, t.IdentityID,
		t.LastMessageAt.Format("2006-01-02T15:04:05Z07:00"),
		t.MessageCount, boolToInt(t.IsRead), boolToInt(t.IsStarred),
		boolToInt(t.IsArchived), boolToInt(t.IsTrashed), t.Snippet,
		t.ParticipantNames,
		t.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		t.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	)
	return err
}

func toJSONString(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// UpsertMessage inserts or updates a message.
func (c *Client) UpsertMessage(ctx context.Context, m *mailpkg.Message) error {
	query := `
		INSERT INTO messages (
			id, thread_id, message_id, in_reply_to, "references",
			from_address, from_name, to_addresses, cc_addresses, bcc_addresses,
			reply_to, subject, body_text, body_html, snippet, original_to,
			direction, imap_uid, resend_id, is_read, is_draft, has_attachments,
			received_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(message_id) DO UPDATE SET
			is_read = excluded.is_read,
			is_draft = excluded.is_draft,
			has_attachments = MAX(messages.has_attachments, excluded.has_attachments),
			imap_uid = COALESCE(excluded.imap_uid, messages.imap_uid)
	`
	boolToInt := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	_, err := c.db.ExecContext(ctx, query,
		m.ID, m.ThreadID, m.MessageID, m.InReplyTo, toJSONString(m.References),
		m.FromAddress, m.FromName, toJSONString(m.ToAddresses), toJSONString(m.CcAddresses), toJSONString(m.BccAddresses),
		m.ReplyTo, m.Subject, m.BodyText, m.BodyHTML, m.Snippet, m.OriginalTo,
		m.Direction, m.IMAPuid, m.ResendID,
		boolToInt(m.IsRead), boolToInt(m.IsDraft), boolToInt(m.HasAttachments),
		m.ReceivedAt.Format("2006-01-02T15:04:05Z07:00"),
		m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	)
	return err
}

// QueueOutbox inserts a message into the outbox for sending.
func (c *Client) QueueOutbox(ctx context.Context, id, messageID, identityID, payload string) error {
	query := `
		INSERT INTO outbox (
			id, message_id, identity_id, payload, status, retry_count, next_retry_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, 'pending', 0, ?, ?, ?)
	`
	now := time.Now().Format(time.RFC3339)
	_, err := c.db.ExecContext(ctx, query, id, messageID, identityID, payload, now, now, now)
	return err
}

// GetPendingOutbox gets outbox items that are ready to send.
func (c *Client) GetPendingOutbox(ctx context.Context) (*sql.Rows, error) {
	query := "SELECT id, message_id, identity_id, payload, retry_count FROM outbox WHERE status = 'pending' AND next_retry_at <= ? ORDER BY created_at ASC"
	return c.db.QueryContext(ctx, query, time.Now().Format(time.RFC3339))
}

// UpdateOutbox updates the status of an outbox item.
func (c *Client) UpdateOutbox(ctx context.Context, id, status, lastError, nextRetryAt string, retryCount int) error {
	query := "UPDATE outbox SET status = ?, last_error = ?, next_retry_at = ?, retry_count = ?, updated_at = ? WHERE id = ?"
	_, err := c.db.ExecContext(ctx, query, status, lastError, nextRetryAt, retryCount, time.Now().Format(time.RFC3339), id)
	return err
}
