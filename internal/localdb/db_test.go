package localdb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	mailpkg "github.com/MiyuLabs/mail/internal/mail"
)

func TestUpsertMessageIsIdempotentByMessageID(t *testing.T) {
	db, err := OpenOrCreate(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.UpsertIdentity(ctx, &mailpkg.Identity{
		ID: "identity", Address: "inbox@example.com", DisplayName: "Inbox",
		Type: mailpkg.IdentityTypeMailbox, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	thread := &mailpkg.Thread{
		ID: "thread", Subject: "Subject", IdentityID: "identity", LastMessageAt: time.Now(),
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := db.UpsertThread(ctx, thread); err != nil {
		t.Fatal(err)
	}

	message := &mailpkg.Message{
		ID: "first-id", ThreadID: thread.ID, MessageID: "<stable@example.com>",
		FromAddress: "sender@example.com", ToAddresses: []string{"inbox@example.com"},
		Subject: "Subject", Direction: mailpkg.DirectionInbound,
		ReceivedAt: time.Now(), CreatedAt: time.Now(),
	}
	if err := db.UpsertMessage(ctx, message); err != nil {
		t.Fatal(err)
	}
	message.ID = "retry-id"
	if err := db.UpsertMessage(ctx, message); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE message_id = ?`, message.MessageID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("message count after retry = %d, want 1", count)
	}
}
