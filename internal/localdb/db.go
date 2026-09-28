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
		mailbox         TEXT NOT NULL DEFAULT 'INBOX',
		uid_validity    INTEGER,
		FOREIGN KEY (thread_id) REFERENCES threads(id)
	);

	CREATE TABLE IF NOT EXISTS mailbox_sync (
		mailbox      TEXT PRIMARY KEY,
		uid_validity INTEGER NOT NULL,
		last_uid     INTEGER NOT NULL DEFAULT 0,
		uid_next     INTEGER NOT NULL DEFAULT 0,
		updated_at   TEXT NOT NULL
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

	// Schema migrations for existing databases
	_, _ = c.db.Exec("ALTER TABLE messages ADD COLUMN mailbox TEXT NOT NULL DEFAULT 'INBOX'")
	_, _ = c.db.Exec("ALTER TABLE messages ADD COLUMN uid_validity INTEGER")
	_, _ = c.db.Exec("CREATE INDEX IF NOT EXISTS idx_messages_mailbox ON messages(mailbox)")
	_, _ = c.db.Exec("CREATE INDEX IF NOT EXISTS idx_messages_uid_validity ON messages(uid_validity)")
	_, _ = c.db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_mailbox_uidvalidity_uid ON messages(mailbox, uid_validity, imap_uid) WHERE imap_uid IS NOT NULL AND imap_uid > 0 AND uid_validity IS NOT NULL AND uid_validity > 0")

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
			received_at, created_at, mailbox, uid_validity
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(message_id) DO UPDATE SET
			is_read = MAX(messages.is_read, excluded.is_read),
			is_draft = messages.is_draft,
			has_attachments = MAX(messages.has_attachments, excluded.has_attachments),
			body_text = CASE WHEN excluded.body_text != '' AND excluded.body_text != '[could not parse message body]' THEN excluded.body_text ELSE messages.body_text END,
			body_html = CASE WHEN excluded.body_html != '' THEN excluded.body_html ELSE messages.body_html END,
			snippet = CASE WHEN excluded.snippet != '' THEN excluded.snippet ELSE messages.snippet END,
			imap_uid = CASE WHEN excluded.imap_uid IS NOT NULL AND excluded.imap_uid > 0 THEN excluded.imap_uid ELSE messages.imap_uid END,
			uid_validity = CASE WHEN excluded.uid_validity IS NOT NULL AND excluded.uid_validity > 0 THEN excluded.uid_validity ELSE messages.uid_validity END,
			mailbox = CASE WHEN excluded.mailbox IS NOT NULL AND excluded.mailbox != '' THEN excluded.mailbox ELSE messages.mailbox END
	`
	boolToInt := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	mailbox := m.Mailbox
	if mailbox == "" {
		mailbox = "INBOX"
	}
	var imapUID interface{}
	if m.IMAPuid > 0 {
		imapUID = m.IMAPuid
	}
	var uidValidity interface{}
	if m.UIDValidity > 0 {
		uidValidity = m.UIDValidity
	}

	_, err := c.db.ExecContext(ctx, query,
		m.ID, m.ThreadID, m.MessageID, m.InReplyTo, toJSONString(m.References),
		m.FromAddress, m.FromName, toJSONString(m.ToAddresses), toJSONString(m.CcAddresses), toJSONString(m.BccAddresses),
		m.ReplyTo, m.Subject, m.BodyText, m.BodyHTML, m.Snippet, m.OriginalTo,
		m.Direction, imapUID, m.ResendID,
		boolToInt(m.IsRead), boolToInt(m.IsDraft), boolToInt(m.HasAttachments),
		m.ReceivedAt.Format("2006-01-02T15:04:05Z07:00"),
		m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		mailbox, uidValidity,
	)
	return err
}

// GetMailboxSync returns the persisted sync state for the given mailbox.
func (c *Client) GetMailboxSync(ctx context.Context, mailbox string) (*mailpkg.MailboxSyncState, error) {
	query := "SELECT mailbox, uid_validity, last_uid, uid_next, updated_at FROM mailbox_sync WHERE mailbox = ?"
	var s mailpkg.MailboxSyncState
	var updated string
	err := c.db.QueryRowContext(ctx, query, mailbox).Scan(
		&s.Mailbox, &s.UIDValidity, &s.LastUID, &s.UIDNext, &updated,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return &s, nil
}

// SetMailboxSync inserts or updates the sync state for the given mailbox.
func (c *Client) SetMailboxSync(ctx context.Context, s *mailpkg.MailboxSyncState) error {
	query := `
		INSERT INTO mailbox_sync (mailbox, uid_validity, last_uid, uid_next, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(mailbox) DO UPDATE SET
			uid_validity = excluded.uid_validity,
			last_uid = excluded.last_uid,
			uid_next = excluded.uid_next,
			updated_at = excluded.updated_at
	`
	updated := s.UpdatedAt.Format(time.RFC3339)
	if s.UpdatedAt.IsZero() {
		updated = time.Now().Format(time.RFC3339)
	}
	_, err := c.db.ExecContext(ctx, query, s.Mailbox, s.UIDValidity, s.LastUID, s.UIDNext, updated)
	return err
}

// InvalidateMailboxUIDs clears the imap_uid and uid_validity for all messages in the given mailbox.
// This is called when a UIDVALIDITY change is detected to prevent stale UID reuse.
func (c *Client) InvalidateMailboxUIDs(ctx context.Context, mailbox string) error {
	query := "UPDATE messages SET imap_uid = NULL, uid_validity = NULL WHERE mailbox = ?"
	_, err := c.db.ExecContext(ctx, query, mailbox)
	return err
}

// GetMailboxUIDs returns all active IMAP UIDs currently associated with the mailbox and uidValidity.
func (c *Client) GetMailboxUIDs(ctx context.Context, mailbox string, uidValidity uint32) ([]uint32, error) {
	query := "SELECT imap_uid FROM messages WHERE mailbox = ? AND uid_validity = ? AND imap_uid IS NOT NULL AND imap_uid > 0 ORDER BY imap_uid ASC"
	rows, err := c.db.QueryContext(ctx, query, mailbox, uidValidity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var uids []uint32
	for rows.Next() {
		var uid uint32
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		uids = append(uids, uid)
	}
	return uids, nil
}

// DeleteMessagesByUIDs deletes messages in the given mailbox and uid_validity matching the provided UIDs.
func (c *Client) DeleteMessagesByUIDs(ctx context.Context, mailbox string, uidValidity uint32, uids []uint32) error {
	if len(uids) == 0 {
		return nil
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, "DELETE FROM messages WHERE mailbox = ? AND uid_validity = ? AND imap_uid = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, uid := range uids {
		if _, err := stmt.ExecContext(ctx, mailbox, uidValidity, uid); err != nil {
			return err
		}
	}

	// Update thread message counts
	_, _ = tx.ExecContext(ctx, `
		UPDATE threads SET
			message_count = (SELECT COUNT(*) FROM messages WHERE thread_id = threads.id),
			is_trashed = CASE WHEN (SELECT COUNT(*) FROM messages WHERE thread_id = threads.id) = 0 THEN 1 ELSE is_trashed END
	`)

	return tx.Commit()
}

// CleanupStaleInboundMessages removes inbound messages for the given mailbox whose UIDs were invalidated and not restored.
func (c *Client) CleanupStaleInboundMessages(ctx context.Context, mailbox string) (int64, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, "DELETE FROM messages WHERE mailbox = ? AND direction = 'inbound' AND (imap_uid IS NULL OR imap_uid = 0)", mailbox)
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()

	// Update thread message counts
	_, _ = tx.ExecContext(ctx, `
		UPDATE threads SET
			message_count = (SELECT COUNT(*) FROM messages WHERE thread_id = threads.id),
			is_trashed = CASE WHEN (SELECT COUNT(*) FROM messages WHERE thread_id = threads.id) = 0 THEN 1 ELSE is_trashed END
	`)

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return affected, nil
}

// GetMessageByUID returns a message by its mailbox, uid_validity, and imap_uid.
func (c *Client) GetMessageByUID(ctx context.Context, mailbox string, uidValidity, uid uint32) (*mailpkg.Message, error) {
	query := `
		SELECT id, thread_id, message_id, in_reply_to, "references",
		from_address, from_name, to_addresses, cc_addresses, bcc_addresses,
		reply_to, subject, body_text, body_html, snippet, original_to,
		direction, imap_uid, resend_id, is_read, is_draft, has_attachments,
		received_at, created_at, mailbox, uid_validity
		FROM messages WHERE mailbox = ? AND uid_validity = ? AND imap_uid = ? LIMIT 1
	`
	row := c.db.QueryRowContext(ctx, query, mailbox, uidValidity, uid)
	var m mailpkg.Message
	var inReplyTo, fromName, replyTo, bodyText, bodyHtml, mSnippet, originalTo, resendID *string
	var imapUID *int
	var uidVal *int
	var refs, toAddrs, ccAddrs, bccAddrs string
	var isRead, isDraft, hasAttachments int
	var recAt, created string
	var mbox string

	if err := row.Scan(
		&m.ID, &m.ThreadID, &m.MessageID, &inReplyTo, &refs,
		&m.FromAddress, &fromName, &toAddrs, &ccAddrs, &bccAddrs,
		&replyTo, &m.Subject, &bodyText, &bodyHtml, &mSnippet, &originalTo,
		&m.Direction, &imapUID, &resendID, &isRead, &isDraft, &hasAttachments,
		&recAt, &created, &mbox, &uidVal,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if inReplyTo != nil {
		m.InReplyTo = *inReplyTo
	}
	if fromName != nil {
		m.FromName = *fromName
	}
	if replyTo != nil {
		m.ReplyTo = *replyTo
	}
	if bodyText != nil {
		m.BodyText = *bodyText
	}
	if bodyHtml != nil {
		m.BodyHTML = *bodyHtml
	}
	if mSnippet != nil {
		m.Snippet = *mSnippet
	}
	if originalTo != nil {
		m.OriginalTo = *originalTo
	}
	if resendID != nil {
		m.ResendID = *resendID
	}
	if imapUID != nil {
		m.IMAPuid = uint32(*imapUID)
	}
	m.Mailbox = mbox
	if uidVal != nil {
		m.UIDValidity = uint32(*uidVal)
	}
	m.IsRead = isRead == 1
	m.IsDraft = isDraft == 1
	m.HasAttachments = hasAttachments == 1
	m.ReceivedAt, _ = time.Parse(time.RFC3339, recAt)
	m.CreatedAt, _ = time.Parse(time.RFC3339, created)
	_ = json.Unmarshal([]byte(refs), &m.References)
	_ = json.Unmarshal([]byte(toAddrs), &m.ToAddresses)
	_ = json.Unmarshal([]byte(ccAddrs), &m.CcAddresses)
	_ = json.Unmarshal([]byte(bccAddrs), &m.BccAddresses)

	return &m, nil
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
