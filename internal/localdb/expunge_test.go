package localdb

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	mailpkg "github.com/MiyuLabs/mail/internal/mail"
)

func TestClearIMAPUIDsPreservesOtherUIDIdentities(t *testing.T) {
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
	for _, uid := range []uint32{11, 22, 33} {
		id := fmt.Sprintf("<message-%d@example.com>", uid)
		m := &mailpkg.Message{
			ID: fmt.Sprintf("message-%d", uid), ThreadID: "thread", MessageID: id,
			FromAddress: "sender@example.com", ToAddresses: []string{"inbox@example.com"},
			Subject: "Subject", Direction: mailpkg.DirectionInbound, IMAPuid: uid,
			ReceivedAt: now, CreatedAt: now,
		}
		if err := db.UpsertMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.ClearIMAPUIDs(ctx, []uint32{22}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		uid   uint32
		valid bool
	}{{11, true}, {22, false}, {33, true}} {
		var got sql.NullInt64
		messageID := fmt.Sprintf("<message-%d@example.com>", want.uid)
		if err := db.db.QueryRowContext(ctx, "SELECT imap_uid FROM messages WHERE message_id = ?", messageID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got.Valid != want.valid || (got.Valid && uint32(got.Int64) != want.uid) {
			t.Errorf("UID for %s = %v, want valid=%v UID=%d", messageID, got, want.valid, want.uid)
		}
	}
}
