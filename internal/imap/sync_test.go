package imap_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	goimap "github.com/emersion/go-imap"

	"github.com/MiyuLabs/mail/internal/imap"
	"github.com/MiyuLabs/mail/internal/localdb"
	mailpkg "github.com/MiyuLabs/mail/internal/mail"
	"github.com/MiyuLabs/mail/internal/store"
)

type mockMessage struct {
	UID       uint32
	MessageID string
	Subject   string
	From      string
	To        string
	Body      string
	Date      time.Time
	Flags     []string
}

type mockIMAPClient struct {
	mu           sync.Mutex
	Mailbox      string
	UIDValidity  uint32
	UIDNext      uint32
	Messages     map[uint32]*mockMessage
	ArchiveCalls [][]uint32
	TrashCalls   [][]uint32
	FlaggedCalls []uint32

	fetchErrAtCount int
	fetchErr        error
}

func newMockIMAPClient(uidValidity uint32) *mockIMAPClient {
	return &mockIMAPClient{
		Mailbox:     "INBOX",
		UIDValidity: uidValidity,
		UIDNext:     1,
		Messages:    make(map[uint32]*mockMessage),
	}
}

func (m *mockIMAPClient) AddMessage(uid uint32, messageID, subject, body string, flags ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Messages[uid] = &mockMessage{
		UID:       uid,
		MessageID: messageID,
		Subject:   subject,
		From:      "sender@example.com",
		To:        "user@example.com",
		Body:      body,
		Date:      time.Now(),
		Flags:     flags,
	}
	if uid >= m.UIDNext {
		m.UIDNext = uid + 1
	}
}

func (m *mockIMAPClient) RemoveMessage(uid uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Messages, uid)
}

func (m *mockIMAPClient) SelectMailbox(name string) (*goimap.MailboxStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &goimap.MailboxStatus{
		Name:        name,
		UidValidity: m.UIDValidity,
		UidNext:     m.UIDNext,
		Messages:    uint32(len(m.Messages)),
	}, nil
}

func (m *mockIMAPClient) SelectInbox() (*goimap.MailboxStatus, error) {
	return m.SelectMailbox("INBOX")
}

func (m *mockIMAPClient) SearchNewUIDs(lastUID uint32) ([]uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var uids []uint32
	for uid := range m.Messages {
		if uid > lastUID {
			uids = append(uids, uid)
		}
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	return uids, nil
}

func (m *mockIMAPClient) SearchAllUIDs() ([]uint32, error) {
	return m.SearchNewUIDs(0)
}

func (m *mockIMAPClient) FetchMessages(uids []uint32, ch chan *goimap.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer close(ch)

	count := 0
	for _, uid := range uids {
		msg, ok := m.Messages[uid]
		if !ok {
			continue
		}
		count++
		if m.fetchErr != nil && count > m.fetchErrAtCount {
			return m.fetchErr
		}

		rawRFC822 := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: <%s>\r\nDate: %s\r\n\r\n%s",
			msg.From, msg.To, msg.Subject, msg.MessageID, msg.Date.Format(time.RFC1123Z), msg.Body)

		imapMsg := &goimap.Message{
			Uid:   msg.UID,
			Flags: msg.Flags,
			Envelope: &goimap.Envelope{
				MessageId: msg.MessageID,
				Subject:   msg.Subject,
				Date:      msg.Date,
			},
			Body: map[*goimap.BodySectionName]goimap.Literal{
				{}: bytes.NewBufferString(rawRFC822),
			},
		}
		ch <- imapMsg
	}
	return nil
}

func (m *mockIMAPClient) FetchMessageHeaders(uids []uint32, ch chan *goimap.Message) error {
	return m.FetchMessages(uids, ch)
}

func (m *mockIMAPClient) SetSeen(uids []uint32, seen bool) error {
	return nil
}

func (m *mockIMAPClient) SetFlagged(ctx context.Context, uids []uint32, flagged bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.FlaggedCalls = append(m.FlaggedCalls, uids...)
	return nil
}

func (m *mockIMAPClient) Archive(ctx context.Context, uids []uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ArchiveCalls = append(m.ArchiveCalls, uids)
	return nil
}

func (m *mockIMAPClient) Trash(ctx context.Context, uids []uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TrashCalls = append(m.TrashCalls, uids)
	return nil
}

func setupTestSyncEngine(t *testing.T, mockClient *mockIMAPClient) (*imap.SyncEngine, *store.Store, *localdb.Client) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	localDB, err := localdb.OpenOrCreate(dbPath)
	if err != nil {
		t.Fatalf("OpenOrCreate failed: %v", err)
	}
	t.Cleanup(func() {
		_ = localDB.Close()
	})

	identity := &mailpkg.Identity{
		ID:          "id-1",
		Address:     "user@example.com",
		DisplayName: "Test User",
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := localDB.UpsertIdentity(context.Background(), identity); err != nil {
		t.Fatalf("UpsertIdentity failed: %v", err)
	}

	appStore := store.NewStore(localDB, nil, mockClient, dir)
	te := mailpkg.NewThreadEngine(
		func(msgID string) (string, bool) { return "", false },
		func(subject string) (string, bool) { return "", false },
		func(thID string) (*mailpkg.Thread, bool) { return nil, false },
	)
	resolver := mailpkg.NewIdentityResolver([]mailpkg.Identity{*identity}, "user@example.com")
	se := imap.NewSyncEngine(mockClient, appStore, te, resolver, dir, "test-client", false, "example.com")
	return se, appStore, localDB
}

// ──────────────────────────────────────────────────────────────────────────────
// Test Scenarios
// ──────────────────────────────────────────────────────────────────────────────

// Scenario 1: Initial mailbox sync
// - persists UIDVALIDITY
// - message sequence numbers are NOT used as persistent identifiers
// - UID mappings correctly stored
func TestScenario1_InitialMailboxSync(t *testing.T) {
	ctx := context.Background()
	mockClient := newMockIMAPClient(1001)
	mockClient.AddMessage(105, "msg-105@example.com", "First Message", "Hello World")
	mockClient.AddMessage(108, "msg-108@example.com", "Second Message", "Hello Again")

	se, _, db := setupTestSyncEngine(t, mockClient)

	if err := se.SyncMailbox(ctx, "INBOX"); err != nil {
		t.Fatalf("SyncMailbox failed: %v", err)
	}

	// Verify UIDVALIDITY persisted
	syncState, err := db.GetMailboxSync(ctx, "INBOX")
	if err != nil || syncState == nil {
		t.Fatalf("GetMailboxSync failed: %v", err)
	}
	if syncState.UIDValidity != 1001 {
		t.Fatalf("expected UIDValidity 1001, got %d", syncState.UIDValidity)
	}
	if syncState.LastUID != 108 {
		t.Fatalf("expected LastUID 108, got %d", syncState.LastUID)
	}

	// Verify message sequence numbers (1, 2) were NOT used as persistent identifiers.
	// The messages MUST be accessible by their actual UIDs (105, 108) scoped by UIDVALIDITY 1001.
	m105, err := db.GetMessageByUID(ctx, "INBOX", 1001, 105)
	if err != nil || m105 == nil {
		t.Fatalf("expected message 105 to be present: %v", err)
	}
	if m105.MessageID != "msg-105@example.com" {
		t.Fatalf("expected msg-105, got %s", m105.MessageID)
	}

	m108, err := db.GetMessageByUID(ctx, "INBOX", 1001, 108)
	if err != nil || m108 == nil {
		t.Fatalf("expected message 108 to be present: %v", err)
	}
	if m108.MessageID != "msg-108@example.com" {
		t.Fatalf("expected msg-108, got %s", m108.MessageID)
	}

	// Verify sequence number 1 or 2 does not return a false match
	mSeq1, _ := db.GetMessageByUID(ctx, "INBOX", 1001, 1)
	if mSeq1 != nil {
		t.Fatalf("sequence number 1 should not match any UID, but got %+v", mSeq1)
	}
}

// Scenario 2 & Scenario 6: Normal incremental sync & New messages added
// - unchanged UIDVALIDITY
// - new messages fetched
// - sync state updated
func TestScenario2_6_IncrementalSyncAndNewMessages(t *testing.T) {
	ctx := context.Background()
	mockClient := newMockIMAPClient(2001)
	mockClient.AddMessage(10, "msg-10@example.com", "Initial", "Body 10")

	se, _, db := setupTestSyncEngine(t, mockClient)

	// Step 1: Initial sync
	if err := se.SyncMailbox(ctx, "INBOX"); err != nil {
		t.Fatalf("Initial SyncMailbox failed: %v", err)
	}

	// Step 2: Add new message to server
	mockClient.AddMessage(20, "msg-20@example.com", "New Mail", "Body 20")

	// Step 3: Incremental sync
	if err := se.SyncMailbox(ctx, "INBOX"); err != nil {
		t.Fatalf("Incremental SyncMailbox failed: %v", err)
	}

	// Check updated state
	syncState, _ := db.GetMailboxSync(ctx, "INBOX")
	if syncState.LastUID != 20 {
		t.Fatalf("expected LastUID 20, got %d", syncState.LastUID)
	}

	uids, _ := db.GetMailboxUIDs(ctx, "INBOX", 2001)
	if len(uids) != 2 || uids[0] != 10 || uids[1] != 20 {
		t.Fatalf("expected UIDs [10, 20], got %v", uids)
	}
}

// Scenario 3 & Scenario 8: Unchanged UIDVALIDITY & Repeated sync (Idempotency)
// - verifies existing message mappings remain valid
// - zero duplicate messages created across multiple identical sync runs
func TestScenario3_8_IdempotencyAndRepeatedSync(t *testing.T) {
	ctx := context.Background()
	mockClient := newMockIMAPClient(3001)
	mockClient.AddMessage(1, "msg-1@example.com", "First", "Body 1")
	mockClient.AddMessage(2, "msg-2@example.com", "Second", "Body 2")

	se, _, db := setupTestSyncEngine(t, mockClient)

	// Run sync 3 times in a row
	for i := 1; i <= 3; i++ {
		if err := se.SyncMailbox(ctx, "INBOX"); err != nil {
			t.Fatalf("Sync run %d failed: %v", i, err)
		}
	}

	uids, err := db.GetMailboxUIDs(ctx, "INBOX", 3001)
	if err != nil {
		t.Fatalf("GetMailboxUIDs failed: %v", err)
	}
	if len(uids) != 2 {
		t.Fatalf("expected exactly 2 messages after repeated syncs, got %d", len(uids))
	}

	// Verify sync state
	syncState, _ := db.GetMailboxSync(ctx, "INBOX")
	if syncState.LastUID != 2 || syncState.UIDValidity != 3001 {
		t.Fatalf("unexpected sync state: %+v", syncState)
	}
}

// Scenario 4: UIDVALIDITY changed
// - server reports different UIDVALIDITY
// - change detected and logged
// - stale UID mappings invalidated
// - mailbox resynchronized under new UIDVALIDITY
// - new UIDs mapped to existing messages without creating duplicate message records
// - messages no longer on server handled safely
// - old UIDs never leaked into IMAP mutation operations
func TestScenario4_UIDValidityChanged(t *testing.T) {
	ctx := context.Background()
	mockClient := newMockIMAPClient(4001)
	mockClient.AddMessage(1, "msg-persist@example.com", "Important Subject", "Content 1")
	mockClient.AddMessage(2, "msg-deleted@example.com", "Disposable", "Content 2")

	se, appStore, db := setupTestSyncEngine(t, mockClient)

	// Step 1: Initial sync under UIDVALIDITY 4001
	if err := se.SyncMailbox(ctx, "INBOX"); err != nil {
		t.Fatalf("Initial sync failed: %v", err)
	}

	// Verify msg-persist is initially read=false, then user reads it locally
	msg1, _ := db.GetMessageByUID(ctx, "INBOX", 4001, 1)
	if msg1 == nil {
		t.Fatal("msg-persist not found")
	}
	threadID := msg1.ThreadID
	if err := appStore.MarkThreadRead(ctx, threadID); err != nil {
		t.Fatalf("MarkThreadRead failed: %v", err)
	}

	// Step 2: Server resets UIDVALIDITY to 4002!
	// Message 1 is now UID 55 on server.
	// Message 2 was deleted from server!
	// Message 3 is a newly arrived message with UID 56.
	mockClient.mu.Lock()
	mockClient.UIDValidity = 4002
	mockClient.Messages = make(map[uint32]*mockMessage)
	mockClient.UIDNext = 57
	mockClient.mu.Unlock()

	mockClient.AddMessage(55, "msg-persist@example.com", "Important Subject", "Content 1")
	mockClient.AddMessage(56, "msg-new@example.com", "Brand New", "Content 3")

	// Step 3: Trigger sync with new UIDVALIDITY
	if err := se.SyncMailbox(ctx, "INBOX"); err != nil {
		t.Fatalf("Rebuild sync failed: %v", err)
	}

	// Verify sync state updated to 4002
	syncState, _ := db.GetMailboxSync(ctx, "INBOX")
	if syncState.UIDValidity != 4002 {
		t.Fatalf("expected UIDValidity 4002, got %d", syncState.UIDValidity)
	}
	if syncState.LastUID != 56 {
		t.Fatalf("expected LastUID 56, got %d", syncState.LastUID)
	}

	// Verify msg-persist mapped to new UID 55 under 4002
	mRebuilt, err := db.GetMessageByUID(ctx, "INBOX", 4002, 55)
	if err != nil || mRebuilt == nil {
		t.Fatalf("expected msg-persist with UID 55: %v", err)
	}
	// Verify local metadata preserved: IsRead is still true, ThreadID is preserved
	if !mRebuilt.IsRead {
		t.Fatalf("expected preserved local IsRead=true, got false")
	}
	if mRebuilt.ThreadID != threadID {
		t.Fatalf("expected preserved ThreadID %s, got %s", threadID, mRebuilt.ThreadID)
	}

	// Verify stale message 2 was cleaned up and is not accessible
	oldM2, _ := db.GetMessageByUID(ctx, "INBOX", 4001, 2)
	if oldM2 != nil {
		t.Fatalf("stale message 2 under 4001 should be gone, but found %+v", oldM2)
	}

	// Verify active UIDs in INBOX under 4002 are [55, 56]
	uids, _ := db.GetMailboxUIDs(ctx, "INBOX", 4002)
	if len(uids) != 2 || uids[0] != 55 || uids[1] != 56 {
		t.Fatalf("expected [55, 56], got %v", uids)
	}

	// Verify old UIDs are never leaked into IMAP mutation operations
	if err := appStore.MarkThreadArchived(ctx, threadID); err != nil {
		t.Fatalf("MarkThreadArchived failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // Allow async IMAP call

	mockClient.mu.Lock()
	archiveCalls := mockClient.ArchiveCalls
	mockClient.mu.Unlock()

	if len(archiveCalls) == 0 {
		t.Fatal("expected Archive call, got none")
	}
	for _, callUIDs := range archiveCalls {
		for _, uid := range callUIDs {
			if uid == 1 {
				t.Fatalf("STALE UID 1 was leaked to IMAP Archive call: %v", callUIDs)
			}
			if uid == 55 {
				// Expected new UID
			}
		}
	}
}

// Scenario 5: Empty mailbox sync
// - succeeds without errors
// - sync state saved
func TestScenario5_EmptyMailboxSync(t *testing.T) {
	ctx := context.Background()
	mockClient := newMockIMAPClient(5001)

	se, _, db := setupTestSyncEngine(t, mockClient)

	if err := se.SyncMailbox(ctx, "INBOX"); err != nil {
		t.Fatalf("Empty mailbox sync failed: %v", err)
	}

	syncState, err := db.GetMailboxSync(ctx, "INBOX")
	if err != nil || syncState == nil {
		t.Fatalf("GetMailboxSync failed: %v", err)
	}
	if syncState.UIDValidity != 5001 || syncState.LastUID != 0 {
		t.Fatalf("unexpected sync state for empty mailbox: %+v", syncState)
	}

	uids, _ := db.GetMailboxUIDs(ctx, "INBOX", 5001)
	if len(uids) != 0 {
		t.Fatalf("expected 0 UIDs, got %v", uids)
	}
}

// Scenario 7: Deleted messages
// - messages deleted on server removed locally
func TestScenario7_DeletedMessagesReconciled(t *testing.T) {
	ctx := context.Background()
	mockClient := newMockIMAPClient(7001)
	mockClient.AddMessage(1, "msg-1@example.com", "Msg 1", "Body 1")
	mockClient.AddMessage(2, "msg-2@example.com", "Msg 2", "Body 2")
	mockClient.AddMessage(3, "msg-3@example.com", "Msg 3", "Body 3")

	se, _, db := setupTestSyncEngine(t, mockClient)

	// Initial sync
	if err := se.SyncMailbox(ctx, "INBOX"); err != nil {
		t.Fatalf("Initial sync failed: %v", err)
	}

	// Delete message 2 on server
	mockClient.RemoveMessage(2)

	// Incremental sync
	if err := se.SyncMailbox(ctx, "INBOX"); err != nil {
		t.Fatalf("Incremental sync failed: %v", err)
	}

	uids, _ := db.GetMailboxUIDs(ctx, "INBOX", 7001)
	if len(uids) != 2 || uids[0] != 1 || uids[1] != 3 {
		t.Fatalf("expected UIDs [1, 3] after deletion, got %v", uids)
	}

	m2, _ := db.GetMessageByUID(ctx, "INBOX", 7001, 2)
	if m2 != nil {
		t.Fatalf("expected message 2 to be deleted locally, got %+v", m2)
	}
}

// Scenario 9: Interrupted sync followed by recovery
// - resume or restart picks up without corruption or duplicates
func TestScenario9_InterruptedSyncRecovery(t *testing.T) {
	ctx := context.Background()
	mockClient := newMockIMAPClient(9001)
	mockClient.AddMessage(1, "msg-1@example.com", "Msg 1", "Body 1")
	mockClient.AddMessage(2, "msg-2@example.com", "Msg 2", "Body 2")
	mockClient.AddMessage(3, "msg-3@example.com", "Msg 3", "Body 3")
	mockClient.AddMessage(4, "msg-4@example.com", "Msg 4", "Body 4")

	// Simulate error after 2nd message
	mockClient.fetchErrAtCount = 2
	mockClient.fetchErr = errors.New("simulated network connection dropped")

	se, _, db := setupTestSyncEngine(t, mockClient)

	// First sync attempt will fail partway through
	err := se.SyncMailbox(ctx, "INBOX")
	if err == nil {
		t.Fatal("expected error on interrupted sync, got nil")
	}

	// Verify messages 1 and 2 were successfully saved and sync state updated to 2
	syncState, _ := db.GetMailboxSync(ctx, "INBOX")
	if syncState == nil || syncState.LastUID != 2 {
		t.Fatalf("expected LastUID 2 after interruption, got %+v", syncState)
	}

	// Clear error on mock server (network restored)
	mockClient.mu.Lock()
	mockClient.fetchErr = nil
	mockClient.mu.Unlock()

	// Second sync attempt (recovery)
	if err := se.SyncMailbox(ctx, "INBOX"); err != nil {
		t.Fatalf("Recovery sync failed: %v", err)
	}

	// Verify all 4 messages now exist and zero duplicates
	uids, _ := db.GetMailboxUIDs(ctx, "INBOX", 9001)
	if len(uids) != 4 || uids[0] != 1 || uids[1] != 2 || uids[2] != 3 || uids[3] != 4 {
		t.Fatalf("expected UIDs [1, 2, 3, 4], got %v", uids)
	}

	syncStateFinal, _ := db.GetMailboxSync(ctx, "INBOX")
	if syncStateFinal.LastUID != 4 {
		t.Fatalf("expected final LastUID 4, got %d", syncStateFinal.LastUID)
	}
}

// Scenario 10: Non-contiguous UIDs
// - server returns UIDs with gaps (e.g., 10, 11, 17, 25)
// - all messages processed correctly without missing or fake records
func TestScenario10_NonContiguousUIDs(t *testing.T) {
	ctx := context.Background()
	mockClient := newMockIMAPClient(10001)
	mockClient.AddMessage(10, "msg-10@example.com", "Msg 10", "Body 10")
	mockClient.AddMessage(11, "msg-11@example.com", "Msg 11", "Body 11")
	mockClient.AddMessage(17, "msg-17@example.com", "Msg 17", "Body 17")
	mockClient.AddMessage(25, "msg-25@example.com", "Msg 25", "Body 25")

	se, _, db := setupTestSyncEngine(t, mockClient)

	if err := se.SyncMailbox(ctx, "INBOX"); err != nil {
		t.Fatalf("SyncMailbox failed: %v", err)
	}

	uids, _ := db.GetMailboxUIDs(ctx, "INBOX", 10001)
	if len(uids) != 4 {
		t.Fatalf("expected exactly 4 UIDs, got %d: %v", len(uids), uids)
	}
	expected := []uint32{10, 11, 17, 25}
	for i, exp := range expected {
		if uids[i] != exp {
			t.Fatalf("at index %d: expected UID %d, got %d", i, exp, uids[i])
		}
	}

	syncState, _ := db.GetMailboxSync(ctx, "INBOX")
	if syncState.LastUID != 25 {
		t.Fatalf("expected LastUID 25, got %d", syncState.LastUID)
	}
}
