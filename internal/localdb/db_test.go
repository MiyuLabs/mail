package localdb_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MiyuLabs/mail/internal/localdb"
	mailpkg "github.com/MiyuLabs/mail/internal/mail"
)

func setupTestDB(t *testing.T) *localdb.Client {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := localdb.OpenOrCreate(dbPath)
	if err != nil {
		t.Fatalf("OpenOrCreate failed: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestMailboxSyncState(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Initial get should return nil
	state, err := db.GetMailboxSync(ctx, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxSync failed: %v", err)
	}
	if state != nil {
		t.Fatalf("expected nil state, got %+v", state)
	}

	// Set state
	now := time.Now().Truncate(time.Second)
	initial := &mailpkg.MailboxSyncState{
		Mailbox:     "INBOX",
		UIDValidity: 12345,
		LastUID:     10,
		UIDNext:     11,
		UpdatedAt:   now,
	}
	if err := db.SetMailboxSync(ctx, initial); err != nil {
		t.Fatalf("SetMailboxSync failed: %v", err)
	}

	// Read back state
	got, err := db.GetMailboxSync(ctx, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxSync after set failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil state")
	}
	if got.Mailbox != "INBOX" || got.UIDValidity != 12345 || got.LastUID != 10 || got.UIDNext != 11 {
		t.Fatalf("unexpected state: %+v", got)
	}

	// Update state
	updated := &mailpkg.MailboxSyncState{
		Mailbox:     "INBOX",
		UIDValidity: 12345,
		LastUID:     25,
		UIDNext:     26,
		UpdatedAt:   now.Add(time.Minute),
	}
	if err := db.SetMailboxSync(ctx, updated); err != nil {
		t.Fatalf("SetMailboxSync update failed: %v", err)
	}

	gotUpdated, err := db.GetMailboxSync(ctx, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxSync after update failed: %v", err)
	}
	if gotUpdated.LastUID != 25 || gotUpdated.UIDNext != 26 {
		t.Fatalf("unexpected updated state: %+v", gotUpdated)
	}
}

func TestMessageUIDValidityAndInvalidation(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Insert identity
	identity := &mailpkg.Identity{
		ID:          "id-1",
		Address:     "test@example.com",
		DisplayName: "Test User",
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.UpsertIdentity(ctx, identity); err != nil {
		t.Fatalf("UpsertIdentity failed: %v", err)
	}

	// Insert thread
	thread := &mailpkg.Thread{
		ID:            "thread-1",
		Subject:       "Test Thread",
		IdentityID:    identity.ID,
		LastMessageAt: time.Now(),
		MessageCount:  2,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := db.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}

	// Insert two messages under UIDVALIDITY 100
	msg1 := &mailpkg.Message{
		ID:          uuid.New().String(),
		ThreadID:    thread.ID,
		MessageID:   "msg-1@example.com",
		Subject:     "Msg 1",
		Direction:   mailpkg.DirectionInbound,
		Mailbox:     "INBOX",
		UIDValidity: 100,
		IMAPuid:     1,
		ReceivedAt:  time.Now(),
		CreatedAt:   time.Now(),
	}
	msg2 := &mailpkg.Message{
		ID:          uuid.New().String(),
		ThreadID:    thread.ID,
		MessageID:   "msg-2@example.com",
		Subject:     "Msg 2",
		Direction:   mailpkg.DirectionInbound,
		Mailbox:     "INBOX",
		UIDValidity: 100,
		IMAPuid:     2,
		ReceivedAt:  time.Now(),
		CreatedAt:   time.Now(),
	}

	if err := db.UpsertMessage(ctx, msg1); err != nil {
		t.Fatalf("UpsertMessage 1 failed: %v", err)
	}
	if err := db.UpsertMessage(ctx, msg2); err != nil {
		t.Fatalf("UpsertMessage 2 failed: %v", err)
	}

	// Verify GetMailboxUIDs
	uids, err := db.GetMailboxUIDs(ctx, "INBOX", 100)
	if err != nil {
		t.Fatalf("GetMailboxUIDs failed: %v", err)
	}
	if len(uids) != 2 || uids[0] != 1 || uids[1] != 2 {
		t.Fatalf("expected [1, 2], got %v", uids)
	}

	// Verify GetMessageByUID
	m, err := db.GetMessageByUID(ctx, "INBOX", 100, 1)
	if err != nil {
		t.Fatalf("GetMessageByUID failed: %v", err)
	}
	if m == nil || m.MessageID != "msg-1@example.com" {
		t.Fatalf("expected msg-1, got %+v", m)
	}

	// Invalidate UIDs for INBOX
	if err := db.InvalidateMailboxUIDs(ctx, "INBOX"); err != nil {
		t.Fatalf("InvalidateMailboxUIDs failed: %v", err)
	}

	// Querying by old UIDVALIDITY should return nothing
	uidsAfter, err := db.GetMailboxUIDs(ctx, "INBOX", 100)
	if err != nil {
		t.Fatalf("GetMailboxUIDs after invalidate failed: %v", err)
	}
	if len(uidsAfter) != 0 {
		t.Fatalf("expected 0 UIDs, got %v", uidsAfter)
	}

	// GetMessageByUID should now return nil
	mAfter, err := db.GetMessageByUID(ctx, "INBOX", 100, 1)
	if err != nil {
		t.Fatalf("GetMessageByUID after invalidate failed: %v", err)
	}
	if mAfter != nil {
		t.Fatalf("expected nil message after invalidate, got %+v", mAfter)
	}

	// Rebuild msg1 under new UIDVALIDITY 200 with new UID 42
	msg1Rebuilt := &mailpkg.Message{
		ID:          uuid.New().String(), // new transient ID
		ThreadID:    "dummy",
		MessageID:   "msg-1@example.com", // same Message-ID
		Subject:     "Msg 1",
		Direction:   mailpkg.DirectionInbound,
		Mailbox:     "INBOX",
		UIDValidity: 200,
		IMAPuid:     42,
		ReceivedAt:  time.Now(),
		CreatedAt:   time.Now(),
	}
	if err := db.UpsertMessage(ctx, msg1Rebuilt); err != nil {
		t.Fatalf("UpsertMessage rebuilt failed: %v", err)
	}

	// Check that msg1 is now associated with (200, 42) and thread-1 is preserved
	rebuiltM, err := db.GetMessageByUID(ctx, "INBOX", 200, 42)
	if err != nil {
		t.Fatalf("GetMessageByUID after rebuild failed: %v", err)
	}
	if rebuiltM == nil {
		t.Fatal("expected rebuilt message, got nil")
	}
	if rebuiltM.ThreadID != "thread-1" {
		t.Fatalf("expected preserved threadID thread-1, got %s", rebuiltM.ThreadID)
	}

	// Clean up stale inbound messages (msg2 was not rebuilt)
	staleCount, err := db.CleanupStaleInboundMessages(ctx, "INBOX")
	if err != nil {
		t.Fatalf("CleanupStaleInboundMessages failed: %v", err)
	}
	if staleCount != 1 {
		t.Fatalf("expected 1 stale message cleaned up, got %d", staleCount)
	}
}

func TestDeleteMessagesByUIDs(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	identity := &mailpkg.Identity{
		ID:          "id-del",
		Address:     "del@example.com",
		DisplayName: "Delete User",
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.UpsertIdentity(ctx, identity); err != nil {
		t.Fatalf("UpsertIdentity failed: %v", err)
	}

	thread := &mailpkg.Thread{
		ID:            "thread-del",
		Subject:       "To Delete",
		IdentityID:    identity.ID,
		LastMessageAt: time.Now(),
		MessageCount:  1,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := db.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}

	msg := &mailpkg.Message{
		ID:          uuid.New().String(),
		ThreadID:    thread.ID,
		MessageID:   "delete-me@example.com",
		Subject:     "To Delete",
		Direction:   mailpkg.DirectionInbound,
		Mailbox:     "INBOX",
		UIDValidity: 50,
		IMAPuid:     7,
		ReceivedAt:  time.Now(),
		CreatedAt:   time.Now(),
	}
	if err := db.UpsertMessage(ctx, msg); err != nil {
		t.Fatalf("UpsertMessage failed: %v", err)
	}

	// Delete by UID
	if err := db.DeleteMessagesByUIDs(ctx, "INBOX", 50, []uint32{7}); err != nil {
		t.Fatalf("DeleteMessagesByUIDs failed: %v", err)
	}

	uids, err := db.GetMailboxUIDs(ctx, "INBOX", 50)
	if err != nil {
		t.Fatalf("GetMailboxUIDs failed: %v", err)
	}
	if len(uids) != 0 {
		t.Fatalf("expected empty UIDs, got %v", uids)
	}
}
