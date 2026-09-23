package localdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	mailpkg "github.com/MiyuLabs/mail/internal/mail"
)

// ListIdentities returns all identities.
func (c *Client) ListIdentities(ctx context.Context) ([]mailpkg.Identity, error) {
	rows, err := c.db.QueryContext(ctx, "SELECT id, address, display_name, identity_type, is_active, created_at, updated_at FROM identities")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []mailpkg.Identity
	for rows.Next() {
		var id mailpkg.Identity
		var isActive int
		var created, updated string
		if err := rows.Scan(&id.ID, &id.Address, &id.DisplayName, &id.Type, &isActive, &created, &updated); err != nil {
			return nil, err
		}
		id.IsActive = isActive == 1
		id.CreatedAt, _ = time.Parse(time.RFC3339, created)
		id.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		ids = append(ids, id)
	}
	return ids, nil
}

// ListThreads returns a page of threads.
func (c *Client) ListThreads(ctx context.Context, req mailpkg.PageRequest) (*mailpkg.ThreadPage, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = mailpkg.DefaultPageSize
	}

	query := "SELECT id, subject, identity_id, last_message_at, message_count, is_read, is_starred, is_archived, is_trashed, snippet, participant_names, created_at, updated_at FROM threads WHERE is_archived = 0 AND is_trashed = 0"
	var args []interface{}

	if req.IdentityID != "" {
		query += " AND identity_id = ?"
		args = append(args, req.IdentityID)
	}
	if req.Cursor != "" {
		query += " AND last_message_at < ?"
		args = append(args, req.Cursor)
	}

	query += " ORDER BY last_message_at DESC LIMIT ?"
	args = append(args, limit+1)

	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []mailpkg.Thread
	for rows.Next() {
		var t mailpkg.Thread
		var isRead, isStarred, isArchived, isTrashed int
		var lastMsg, created, updated string
		var snippet, partNames *string
		if err := rows.Scan(
			&t.ID, &t.Subject, &t.IdentityID, &lastMsg, &t.MessageCount,
			&isRead, &isStarred, &isArchived, &isTrashed,
			&snippet, &partNames, &created, &updated,
		); err != nil {
			return nil, err
		}
		t.LastMessageAt, _ = time.Parse(time.RFC3339, lastMsg)
		t.IsRead = isRead == 1
		t.IsStarred = isStarred == 1
		t.IsArchived = isArchived == 1
		t.IsTrashed = isTrashed == 1
		if snippet != nil {
			t.Snippet = *snippet
		}
		if partNames != nil {
			t.ParticipantNames = *partNames
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339, created)
		t.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		threads = append(threads, t)
	}

	page := &mailpkg.ThreadPage{}
	if len(threads) > limit {
		page.NextCursor = threads[limit].LastMessageAt.Format(time.RFC3339Nano)
		page.Threads = threads[:limit]
	} else {
		page.Threads = threads
	}
	return page, nil
}

// SearchParticipants searches participants by prefix.
func (c *Client) SearchParticipants(ctx context.Context, prefix string) ([]string, error) {
	if prefix == "" {
		return nil, nil
	}
	query := "SELECT display_name, address FROM participants WHERE address LIKE ? OR display_name LIKE ? ORDER BY last_seen_at DESC LIMIT 5"
	pattern := prefix + "%"
	rows, err := c.db.QueryContext(ctx, query, pattern, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []string
	for rows.Next() {
		var name, address string
		if err := rows.Scan(&name, &address); err != nil {
			return nil, err
		}
		if name != "" {
			results = append(results, fmt.Sprintf("%s <%s>", name, address))
		} else {
			results = append(results, address)
		}
	}
	return results, nil
}

// SearchThreads searches threads by a query string.
func (c *Client) SearchThreads(ctx context.Context, req mailpkg.PageRequest, query string) (*mailpkg.ThreadPage, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = mailpkg.DefaultPageSize
	}

	// Basic LIKE search across subject, snippet, and participants.
	// A robust local search would use SQLite FTS5 extension.
	searchPattern := "%" + query + "%"
	
	sqlQuery := "SELECT id, subject, identity_id, last_message_at, message_count, is_read, is_starred, is_archived, is_trashed, snippet, participant_names, created_at, updated_at FROM threads WHERE (subject LIKE ? OR snippet LIKE ? OR participant_names LIKE ?)"
	var args []interface{}
	args = append(args, searchPattern, searchPattern, searchPattern)

	if req.IdentityID != "" {
		sqlQuery += " AND identity_id = ?"
		args = append(args, req.IdentityID)
	}
	if req.Cursor != "" {
		sqlQuery += " AND last_message_at < ?"
		args = append(args, req.Cursor)
	}

	sqlQuery += " ORDER BY last_message_at DESC LIMIT ?"
	args = append(args, limit+1)

	rows, err := c.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []mailpkg.Thread
	for rows.Next() {
		var t mailpkg.Thread
		var isRead, isStarred, isArchived, isTrashed int
		var lastMsg, created, updated string
		var snippet, partNames *string
		if err := rows.Scan(
			&t.ID, &t.Subject, &t.IdentityID, &lastMsg, &t.MessageCount,
			&isRead, &isStarred, &isArchived, &isTrashed,
			&snippet, &partNames, &created, &updated,
		); err != nil {
			return nil, err
		}
		t.LastMessageAt, _ = time.Parse(time.RFC3339, lastMsg)
		t.IsRead = isRead == 1
		t.IsStarred = isStarred == 1
		t.IsArchived = isArchived == 1
		t.IsTrashed = isTrashed == 1
		if snippet != nil {
			t.Snippet = *snippet
		}
		if partNames != nil {
			t.ParticipantNames = *partNames
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339, created)
		t.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		threads = append(threads, t)
	}

	page := &mailpkg.ThreadPage{}
	if len(threads) > limit {
		page.NextCursor = threads[limit].LastMessageAt.Format(time.RFC3339Nano)
		page.Threads = threads[:limit]
	} else {
		page.Threads = threads
	}
	return page, nil
}

// GetThread returns a thread and all its messages.
func (c *Client) GetThread(ctx context.Context, threadID string) (*mailpkg.Thread, error) {
	query := "SELECT id, subject, identity_id, last_message_at, message_count, is_read, is_starred, is_archived, is_trashed, snippet, participant_names, created_at, updated_at FROM threads WHERE id = ?"
	row := c.db.QueryRowContext(ctx, query, threadID)

	var t mailpkg.Thread
	var isRead, isStarred, isArchived, isTrashed int
	var lastMsg, created, updated string
	var snippet, partNames *string
	if err := row.Scan(
		&t.ID, &t.Subject, &t.IdentityID, &lastMsg, &t.MessageCount,
		&isRead, &isStarred, &isArchived, &isTrashed,
		&snippet, &partNames, &created, &updated,
	); err != nil {
		return nil, err
	}
	t.LastMessageAt, _ = time.Parse(time.RFC3339, lastMsg)
	t.IsRead = isRead == 1
	t.IsStarred = isStarred == 1
	t.IsArchived = isArchived == 1
	t.IsTrashed = isTrashed == 1
	if snippet != nil {
		t.Snippet = *snippet
	}
	if partNames != nil {
		t.ParticipantNames = *partNames
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339, created)
	t.UpdatedAt, _ = time.Parse(time.RFC3339, updated)

	// Fetch messages
	msgQuery := `
		SELECT id, thread_id, message_id, in_reply_to, "references",
		from_address, from_name, to_addresses, cc_addresses, bcc_addresses,
		reply_to, subject, body_text, body_html, snippet, original_to,
		direction, imap_uid, resend_id, is_read, is_draft, has_attachments,
		received_at, created_at
		FROM messages WHERE thread_id = ? ORDER BY received_at ASC
	`
	rows, err := c.db.QueryContext(ctx, msgQuery, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var m mailpkg.Message
		var inReplyTo, fromName, replyTo, bodyText, bodyHtml, mSnippet, originalTo, resendID *string
		var imapUID *int
		var refs, toAddrs, ccAddrs, bccAddrs string
		var isRead, isDraft, hasAttachments int
		var recAt, created string

		if err := rows.Scan(
			&m.ID, &m.ThreadID, &m.MessageID, &inReplyTo, &refs,
			&m.FromAddress, &fromName, &toAddrs, &ccAddrs, &bccAddrs,
			&replyTo, &m.Subject, &bodyText, &bodyHtml, &mSnippet, &originalTo,
			&m.Direction, &imapUID, &resendID, &isRead, &isDraft, &hasAttachments,
			&recAt, &created,
		); err != nil {
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
		m.IsRead = isRead == 1
		m.IsDraft = isDraft == 1
		m.HasAttachments = hasAttachments == 1
		m.ReceivedAt, _ = time.Parse(time.RFC3339, recAt)
		m.CreatedAt, _ = time.Parse(time.RFC3339, created)

		json.Unmarshal([]byte(refs), &m.References)
		json.Unmarshal([]byte(toAddrs), &m.ToAddresses)
		json.Unmarshal([]byte(ccAddrs), &m.CcAddresses)
		json.Unmarshal([]byte(bccAddrs), &m.BccAddresses)

		t.Messages = append(t.Messages, m)
	}

	return &t, nil
}

// GetThreadIDByMessageID returns the thread_id for a given message_id.
func (c *Client) GetThreadIDByMessageID(ctx context.Context, messageID string) (string, error) {
	var threadID string
	err := c.db.QueryRowContext(ctx, "SELECT thread_id FROM messages WHERE message_id = ?", messageID).Scan(&threadID)
	return threadID, err
}

// GetThreadIDBySubject looks for the most recent thread with the exact same subject.
func (c *Client) GetThreadIDBySubject(ctx context.Context, subject string) (string, error) {
	var threadID string
	err := c.db.QueryRowContext(ctx, "SELECT id FROM threads WHERE subject = ? COLLATE NOCASE ORDER BY last_message_at DESC LIMIT 1", subject).Scan(&threadID)
	return threadID, err
}

// MarkThreadArchived sets a thread's is_archived flag to 1.
func (c *Client) MarkThreadArchived(ctx context.Context, threadID string) error {
	_, err := c.db.ExecContext(ctx, "UPDATE threads SET is_archived = 1, updated_at = ? WHERE id = ?", time.Now().Format("2006-01-02T15:04:05Z07:00"), threadID)
	return err
}

// MarkThreadRead sets a thread's is_read flag to 1.
func (c *Client) MarkThreadRead(ctx context.Context, threadID string) error {
	_, err := c.db.ExecContext(ctx, "UPDATE threads SET is_read = 1, updated_at = ? WHERE id = ?", time.Now().Format("2006-01-02T15:04:05Z07:00"), threadID)
	return err
}

// MarkThreadStarred sets a thread's is_starred flag.
func (c *Client) MarkThreadStarred(ctx context.Context, threadID string, starred bool) error {
	val := 0
	if starred {
		val = 1
	}
	_, err := c.db.ExecContext(ctx, "UPDATE threads SET is_starred = ? WHERE id = ?", val, threadID)
	return err
}

// MarkThreadTrashed sets a thread's is_trashed flag to 1.
func (c *Client) MarkThreadTrashed(ctx context.Context, threadID string) error {
	_, err := c.db.ExecContext(ctx, "UPDATE threads SET is_trashed = 1 WHERE id = ?", threadID)
	return err
}
