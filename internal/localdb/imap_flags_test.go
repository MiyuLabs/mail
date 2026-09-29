package localdb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	mailpkg "github.com/MiyuLabs/mail/internal/mail"
)

func TestUpdateMessageReadByUIDUpdatesMessageAndThreadAtomically(t *testing.T) {
	db, err := OpenOrCreate(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now()
	if err := db.UpsertIdentity(ctx, &mailpkg.Identity{
		ID: "identity", Address: "inbox@example.com", DisplayName: "Inbox",
		Type: mailpkg.IdentityTypeMailbox, IsActive: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertThread(ctx, &mailpkg.Thread{
		ID: "thread", Subject: "Subject", IdentityID: "identity", LastMessageAt: now,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(ctx, &mailpkg.Message{
		ID: "message", ThreadID: "thread", MessageID: "<message@example.com>",
		FromAddress: "sender@example.com", ToAddresses: []string{"inbox@example.com"},
		Subject: "Subject", Direction: mailpkg.DirectionInbound, IMAPuid: 77,
		ReceivedAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMessageReadByUID(ctx, 77, true); err != nil {
		t.Fatal(err)
	}
	var messageRead, threadRead int
	if err := db.db.QueryRowContext(ctx, "SELECT is_read FROM messages WHERE imap_uid = 77").Scan(&messageRead); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRowContext(ctx, "SELECT is_read FROM threads WHERE id = 'thread'").Scan(&threadRead); err != nil {
		t.Fatal(err)
	}
	if messageRead != 1 || threadRead != 1 {
		t.Fatalf("read state message=%d thread=%d; want both read", messageRead, threadRead)
	}
}
