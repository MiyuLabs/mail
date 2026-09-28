// Package imap — sync.go implements the incremental UID-based sync engine.
// On startup: fetch latest 30 messages fully, then background-sync older messages
// by headers only. On IDLE notification: fetch new UIDs. All metadata is pushed
// to D1; message bodies are cached locally.
package imap

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	goimap "github.com/emersion/go-imap"
	"github.com/google/uuid"

	"github.com/MiyuLabs/mail/internal/d1"
	mailpkg "github.com/MiyuLabs/mail/internal/mail"
	"github.com/MiyuLabs/mail/internal/store"
)

const (
	initialFullFetchCount = 30
)

// SyncEngine orchestrates IMAP sync and D1 persistence.
type SyncEngine struct {
	imap           IMAPClient
	store          *store.Store
	threadEngine   *mailpkg.ThreadEngine
	resolver       *mailpkg.IdentityResolver
	cacheDir       string
	clientID       string
	lastUID        uint32
	filterUnrouted bool // when true, skip messages with no matching identity
	domain         string
}

// NewSyncEngine creates a sync engine.
func NewSyncEngine(
	imapClient IMAPClient,
	store *store.Store,
	te *mailpkg.ThreadEngine,
	resolver *mailpkg.IdentityResolver,
	cacheDir, clientID string,
	filterUnrouted bool,
	domain string,
) *SyncEngine {
	return &SyncEngine{
		imap:           imapClient,
		store:          store,
		threadEngine:   te,
		resolver:       resolver,
		cacheDir:       cacheDir,
		clientID:       clientID,
		filterUnrouted: filterUnrouted,
		domain:         domain,
	}
}

// InitialSync performs the startup sync:
//  1. Pull last sync state from D1.
//  2. Synchronize the INBOX mailbox.
func (se *SyncEngine) InitialSync(ctx context.Context) error {
	// 1. Get last known state from D1 (if available).
	if se.store.Remote() != nil {
		pullResp, err := se.store.Remote().Pull(ctx, d1.PullRequest{ClientID: se.clientID})
		if err != nil {
			log.Printf("sync: D1 pull failed (continuing offline): %v", err)
		} else {
			se.lastUID = pullResp.LastIMAPUID
			// Hydrate the threading engine with existing data.
			if se.threadEngine != nil {
				se.threadEngine.Hydrate(pullResp.Messages, pullResp.Threads)
			}

			// Save pulled state to local cache.
			for _, t := range pullResp.Threads {
				tCopy := t
				_ = se.store.Local().UpsertThread(ctx, &tCopy)
			}
			for _, m := range pullResp.Messages {
				mCopy := m
				_ = se.store.Local().UpsertMessage(ctx, &mCopy)
			}
		}
	}

	// 2. Perform mailbox sync for INBOX.
	return se.SyncMailbox(ctx, "INBOX")
}

// SyncNewMessages fetches messages newer than the current lastUID.
// Called after an IMAP IDLE EXISTS notification.
func (se *SyncEngine) SyncNewMessages(ctx context.Context) error {
	return se.SyncMailbox(ctx, "INBOX")
}

// SyncMailbox performs synchronization for the specified mailbox:
//  1. Selects the mailbox on IMAP and checks UIDVALIDITY.
//  2. Compares against persisted UIDVALIDITY:
//     - If first sync: runs initial sync and persists UIDVALIDITY and UIDNEXT.
//     - If unchanged: runs incremental sync and reconciles deleted messages.
//     - If changed: invalidates stale UIDs, rebuilds mappings from authoritative server state,
//     preserves local metadata by RFC 5322 Message-ID, and cleans up stale messages.
func (se *SyncEngine) SyncMailbox(ctx context.Context, mailbox string) error {
	if mailbox == "" {
		mailbox = "INBOX"
	}

	status, err := se.imap.SelectMailbox(mailbox)
	if err != nil {
		return fmt.Errorf("sync: select mailbox %s: %w", mailbox, err)
	}

	localSync, err := se.store.Local().GetMailboxSync(ctx, mailbox)
	if err != nil {
		log.Printf("sync: could not read local mailbox sync state for %s: %v", mailbox, err)
	}

	if localSync == nil || localSync.UIDValidity == 0 {
		// Case 1: Initial sync
		log.Printf("sync: initial sync for mailbox %s (UIDVALIDITY: %d, UIDNEXT: %d)", mailbox, status.UidValidity, status.UidNext)
		return se.syncInitial(ctx, mailbox, status)
	}

	if localSync.UIDValidity == status.UidValidity {
		// Case 2: Unchanged UIDVALIDITY -> Incremental sync
		log.Printf("sync: incremental sync for mailbox %s (UIDVALIDITY: %d, LastUID: %d, UIDNEXT: %d)", mailbox, status.UidValidity, localSync.LastUID, status.UidNext)
		return se.syncIncremental(ctx, mailbox, status, localSync)
	}

	// Case 3: Changed UIDVALIDITY -> Invalidate & Rebuild
	log.Printf("sync: UIDVALIDITY changed for mailbox %s (previous: %d, new: %d) — invalidating and rebuilding local UID mappings",
		mailbox, localSync.UIDValidity, status.UidValidity)
	return se.syncRebuild(ctx, mailbox, status, localSync)
}

func (se *SyncEngine) syncInitial(ctx context.Context, mailbox string, status *goimap.MailboxStatus) error {
	allUIDs, err := se.imap.SearchAllUIDs()
	if err != nil {
		return fmt.Errorf("sync: search all UIDs for %s: %w", mailbox, err)
	}

	if len(allUIDs) == 0 {
		log.Printf("sync: mailbox %s is empty (UIDVALIDITY: %d)", mailbox, status.UidValidity)
		syncState := &mailpkg.MailboxSyncState{
			Mailbox:     mailbox,
			UIDValidity: status.UidValidity,
			LastUID:     0,
			UIDNext:     status.UidNext,
			UpdatedAt:   time.Now(),
		}
		if err := se.store.Local().SetMailboxSync(ctx, syncState); err != nil {
			return fmt.Errorf("sync: persist mailbox sync: %w", err)
		}
		if mailbox == "INBOX" {
			se.lastUID = 0
		}
		return nil
	}

	sort.Slice(allUIDs, func(i, j int) bool { return allUIDs[i] < allUIDs[j] })
	log.Printf("sync: initial sync found %d messages for mailbox %s", len(allUIDs), mailbox)

	if len(allUIDs) <= initialFullFetchCount {
		return se.fetchAndProcess(ctx, mailbox, status.UidValidity, status.UidNext, allUIDs, true)
	}

	olderUIDs := allUIDs[:len(allUIDs)-initialFullFetchCount]
	fullFetchUIDs := allUIDs[len(allUIDs)-initialFullFetchCount:]

	// Fetch older message headers first to advance state monotonically
	if err := se.fetchAndProcess(ctx, mailbox, status.UidValidity, status.UidNext, olderUIDs, false); err != nil {
		return err
	}
	return se.fetchAndProcess(ctx, mailbox, status.UidValidity, status.UidNext, fullFetchUIDs, true)
}

func (se *SyncEngine) syncIncremental(ctx context.Context, mailbox string, status *goimap.MailboxStatus, localSync *mailpkg.MailboxSyncState) error {
	newUIDs, err := se.imap.SearchNewUIDs(localSync.LastUID)
	if err != nil {
		return fmt.Errorf("sync: search new UIDs for %s: %w", mailbox, err)
	}

	if len(newUIDs) > 0 {
		sort.Slice(newUIDs, func(i, j int) bool { return newUIDs[i] < newUIDs[j] })
		log.Printf("sync: %d new messages to fetch for mailbox %s", len(newUIDs), mailbox)
		if err := se.fetchAndProcess(ctx, mailbox, status.UidValidity, status.UidNext, newUIDs, true); err != nil {
			return err
		}
	} else {
		log.Printf("sync: no new messages for mailbox %s", mailbox)
	}

	// Reconcile deleted messages: compare server UIDs against local UIDs
	allServerUIDs, err := se.imap.SearchAllUIDs()
	if err != nil {
		log.Printf("sync: warning: could not search all UIDs for deletion reconciliation: %v", err)
	} else {
		localUIDs, err := se.store.Local().GetMailboxUIDs(ctx, mailbox, status.UidValidity)
		if err != nil {
			log.Printf("sync: warning: could not get local UIDs for %s: %v", mailbox, err)
		} else {
			serverSet := make(map[uint32]bool, len(allServerUIDs))
			for _, uid := range allServerUIDs {
				serverSet[uid] = true
			}
			var deletedUIDs []uint32
			for _, uid := range localUIDs {
				if !serverSet[uid] {
					deletedUIDs = append(deletedUIDs, uid)
				}
			}
			if len(deletedUIDs) > 0 {
				log.Printf("sync: reconciling %d deleted messages for mailbox %s", len(deletedUIDs), mailbox)
				if err := se.store.Local().DeleteMessagesByUIDs(ctx, mailbox, status.UidValidity, deletedUIDs); err != nil {
					log.Printf("sync: failed to delete removed messages: %v", err)
				}
			}
		}
	}

	// Persist final sync state
	updatedLastUID := localSync.LastUID
	if len(newUIDs) > 0 && newUIDs[len(newUIDs)-1] > updatedLastUID {
		updatedLastUID = newUIDs[len(newUIDs)-1]
	}
	if len(allServerUIDs) == 0 && err == nil {
		updatedLastUID = 0
	}
	uidNext := status.UidNext
	if uidNext == 0 && updatedLastUID > 0 {
		uidNext = updatedLastUID + 1
	}
	syncState := &mailpkg.MailboxSyncState{
		Mailbox:     mailbox,
		UIDValidity: status.UidValidity,
		LastUID:     updatedLastUID,
		UIDNext:     uidNext,
		UpdatedAt:   time.Now(),
	}
	if err := se.store.Local().SetMailboxSync(ctx, syncState); err != nil {
		return fmt.Errorf("sync: update mailbox sync state: %w", err)
	}
	if mailbox == "INBOX" {
		se.lastUID = updatedLastUID
	}

	return nil
}

func (se *SyncEngine) syncRebuild(ctx context.Context, mailbox string, status *goimap.MailboxStatus, localSync *mailpkg.MailboxSyncState) error {
	log.Printf("sync: UIDVALIDITY changed for mailbox %s (previous: %d, new: %d) — invalidating stale UID mappings",
		mailbox, localSync.UIDValidity, status.UidValidity)

	// Step 1: Invalidate stale UIDs for this mailbox
	if err := se.store.Local().InvalidateMailboxUIDs(ctx, mailbox); err != nil {
		return fmt.Errorf("sync: invalidate stale UIDs: %w", err)
	}

	// Step 2: Fetch all UIDs under new UIDVALIDITY from server
	allUIDs, err := se.imap.SearchAllUIDs()
	if err != nil {
		return fmt.Errorf("sync: search all UIDs for rebuild: %w", err)
	}
	sort.Slice(allUIDs, func(i, j int) bool { return allUIDs[i] < allUIDs[j] })

	// Step 3: Fetch and process messages under new UIDVALIDITY
	if len(allUIDs) > 0 {
		log.Printf("sync: rebuilding %d messages for mailbox %s under new UIDVALIDITY %d", len(allUIDs), mailbox, status.UidValidity)
		if err := se.fetchAndProcess(ctx, mailbox, status.UidValidity, status.UidNext, allUIDs, true); err != nil {
			return fmt.Errorf("sync: fetch and rebuild messages for %s: %w", mailbox, err)
		}
	}

	// Step 4: Clean up stale inbound messages that were not present on the server
	staleCount, err := se.store.Local().CleanupStaleInboundMessages(ctx, mailbox)
	if err != nil {
		log.Printf("sync: warning: failed to clean up stale inbound messages for %s: %v", mailbox, err)
	}

	// Step 5: Persist updated sync state
	var maxUID uint32
	if len(allUIDs) > 0 {
		maxUID = allUIDs[len(allUIDs)-1]
	}
	uidNext := status.UidNext
	if uidNext == 0 && maxUID > 0 {
		uidNext = maxUID + 1
	}

	newSyncState := &mailpkg.MailboxSyncState{
		Mailbox:     mailbox,
		UIDValidity: status.UidValidity,
		LastUID:     maxUID,
		UIDNext:     uidNext,
		UpdatedAt:   time.Now(),
	}
	if err := se.store.Local().SetMailboxSync(ctx, newSyncState); err != nil {
		return fmt.Errorf("sync: persist mailbox sync state: %w", err)
	}
	if mailbox == "INBOX" {
		se.lastUID = maxUID
	}

	log.Printf("sync: UIDVALIDITY rebuild complete for mailbox %s: %d messages rebuilt with new UIDs, %d stale inbound messages removed",
		mailbox, len(allUIDs), staleCount)

	return nil
}

// FetchMessageBody fetches the full body of a single message on demand.
// Used when the user opens a thread whose body was not eagerly fetched.
func (se *SyncEngine) FetchMessageBody(ctx context.Context, imapUID uint32) (*mailpkg.Message, error) {
	msgCh := make(chan *goimap.Message, 1)
	if err := se.imap.FetchMessages([]uint32{imapUID}, msgCh); err != nil {
		return nil, fmt.Errorf("sync: fetch body for UID %d: %w", imapUID, err)
	}
	raw, ok := <-msgCh
	if !ok {
		return nil, fmt.Errorf("sync: no message returned for UID %d", imapUID)
	}
	var uidValidity uint32
	if syncState, err := se.store.Local().GetMailboxSync(ctx, "INBOX"); err == nil && syncState != nil {
		uidValidity = syncState.UIDValidity
	}
	return se.processRawMessage(ctx, raw, "INBOX", uidValidity)
}

// ──────────────────────────────────────────────────────────────────────────────
// internal
// ──────────────────────────────────────────────────────────────────────────────

func (se *SyncEngine) fetchAndProcess(ctx context.Context, mailbox string, uidValidity, uidNext uint32, uids []uint32, full bool) error {
	if len(uids) == 0 {
		return nil
	}

	msgCh := make(chan *goimap.Message, 16)
	var fetchErr error

	go func() {
		if full {
			fetchErr = se.imap.FetchMessages(uids, msgCh)
		} else {
			fetchErr = se.imap.FetchMessageHeaders(uids, msgCh)
		}
	}()

	var toSync d1.PushRequest
	toSync.ClientID = se.clientID

	highestUID := se.lastUID
	if mailbox != "INBOX" {
		highestUID = 0
	}

	for raw := range msgCh {
		m, err := se.processRawMessage(ctx, raw, mailbox, uidValidity)
		if err != nil {
			log.Printf("sync: skipping UID %d: %v", raw.Uid, err)
			if raw.Uid > highestUID {
				highestUID = raw.Uid
			}
			continue
		}
		if m == nil {
			// Filtered (not routed)
			if raw.Uid > highestUID {
				highestUID = raw.Uid
			}
			continue
		}

		if raw.Uid > highestUID {
			highestUID = raw.Uid
		}

		mCopy := *m
		if err := se.store.Local().UpsertMessage(ctx, &mCopy); err != nil {
			log.Printf("sync: localdb message upsert failed for UID %d: %v", raw.Uid, err)
		}

		// Update sync state as we advance so interrupted sync can resume cleanly
		_ = se.store.Local().SetMailboxSync(ctx, &mailpkg.MailboxSyncState{
			Mailbox:     mailbox,
			UIDValidity: uidValidity,
			LastUID:     highestUID,
			UIDNext:     uidNext,
			UpdatedAt:   time.Now(),
		})
		if mailbox == "INBOX" {
			se.lastUID = highestUID
		}

		toSync.Messages = append(toSync.Messages, *m)
		toSync.LastIMAPUID = highestUID
	}

	if fetchErr != nil {
		return fmt.Errorf("sync: IMAP fetch error: %w", fetchErr)
	}

	// Push batch to D1 if remote client is configured
	if len(toSync.Messages) > 0 && se.store.Remote() != nil {
		if err := se.store.Remote().Push(ctx, toSync); err != nil {
			log.Printf("sync: D1 push failed (data will be retried): %v", err)
		}
	}

	return nil
}

func (se *SyncEngine) processRawMessage(ctx context.Context, raw *goimap.Message, mailbox string, uidValidity uint32) (*mailpkg.Message, error) {
	// Extract the raw body bytes from BODY[] section.
	var rawBody []byte
	for _, section := range raw.Body {
		data, err := io.ReadAll(section)
		if err != nil {
			return nil, fmt.Errorf("sync: read body section: %w", err)
		}
		rawBody = data
		break
	}

	if len(rawBody) == 0 {
		// Headers-only fetch — synthesize from envelope.
		rawBody = synthesizeFromEnvelope(raw)
	}

	m, err := ParseMessage(raw, rawBody, se.domain)
	if err != nil {
		return nil, fmt.Errorf("sync: parse message: %w", err)
	}

	m.ID = uuid.New().String()
	m.Mailbox = mailbox
	m.UIDValidity = uidValidity
	m.IMAPuid = raw.Uid

	if m.MessageID == "" {
		m.MessageID = fmt.Sprintf("no-message-id-%s-%d-%d@local", mailbox, uidValidity, raw.Uid)
	}

	// Resolve identity.
	var matched bool
	var identity *mailpkg.Identity
	if se.resolver != nil {
		identity, matched = se.resolver.Resolve(m)
	}

	// ── Filter: skip messages not routed via Cloudflare ────────────────────
	// When filterUnrouted is true we only index mail that has an X-Original-To
	// header or whose To/Cc matches a known @domain.tld identity.
	// Direct-to-Gmail mail (no matching identity) is silently skipped.
	if se.filterUnrouted && !matched {
		log.Printf("sync: skipping unrouted message %s (from %s, to %v) — not addressed to a known identity",
			m.MessageID, m.FromAddress, m.ToAddresses)
		return nil, nil
	}

	var identityID string
	if identity != nil {
		identityID = identity.ID
	}

	// Assign to thread if threadEngine is available.
	if se.threadEngine != nil {
		thread, err := se.threadEngine.AssignThread(m, identityID)
		if err != nil {
			log.Printf("sync: threading error for %s: %v", m.MessageID, err)
			thread = &mailpkg.Thread{
				ID:            uuid.New().String(),
				Subject:       mailpkg.NormalizeSubject(m.Subject),
				IdentityID:    identityID,
				LastMessageAt: m.ReceivedAt,
				MessageCount:  1,
				Snippet:       m.Snippet,
				CreatedAt:     time.Now(),
				UpdatedAt:     time.Now(),
			}
		}
		m.ThreadID = thread.ID

		if se.store.Remote() != nil {
			if err := se.store.Remote().UpsertThread(ctx, thread); err != nil {
				log.Printf("sync: D1 thread upsert failed: %v", err)
			}
		}
		if err := se.store.Local().UpsertThread(ctx, thread); err != nil {
			log.Printf("sync: localdb thread upsert failed: %v", err)
		}
	}

	// Cache attachments to local disk.
	if len(m.Attachments) > 0 {
		if err := se.cacheAttachments(m); err != nil {
			log.Printf("sync: attachment cache error for %s: %v", m.MessageID, err)
		}
	}

	return m, nil
}

// synthesizeFromEnvelope builds a minimal RFC 5322 header block from a
// go-imap Envelope (used when only headers were fetched).
func synthesizeFromEnvelope(raw *goimap.Message) []byte {
	if raw.Envelope == nil {
		return nil
	}
	env := raw.Envelope
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Message-ID: <%s>\r\n", env.MessageId)
	fmt.Fprintf(&buf, "Subject: %s\r\n", env.Subject)
	if len(env.From) > 0 {
		fmt.Fprintf(&buf, "From: %s <%s>\r\n", env.From[0].PersonalName, env.From[0].MailboxName+"@"+env.From[0].HostName)
	}
	for _, addr := range env.To {
		fmt.Fprintf(&buf, "To: %s <%s>\r\n", addr.PersonalName, addr.MailboxName+"@"+addr.HostName)
	}
	fmt.Fprintf(&buf, "Date: %s\r\n", env.Date.Format(time.RFC1123Z))
	fmt.Fprintf(&buf, "In-Reply-To: %s\r\n", env.InReplyTo)
	fmt.Fprintf(&buf, "\r\n")
	return buf.Bytes()
}

// cacheAttachments writes attachment binary data to local disk and sets StorageKey.
func (se *SyncEngine) cacheAttachments(m *mailpkg.Message) error {
	for i := range m.Attachments {
		att := &m.Attachments[i]
		if len(att.RawData) == 0 {
			continue
		}
		att.ID = uuid.New().String()
		att.MessageID = m.ID
		dir := filepath.Join(se.cacheDir, "attachments", m.MessageID)

		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("sync: create attachment dir: %w", err)
		}

		att.StorageKey = filepath.Join(dir, att.Filename)

		if err := os.WriteFile(att.StorageKey, att.RawData, 0644); err != nil {
			return fmt.Errorf("sync: write attachment to disk: %w", err)
		}

		// Clear raw data after writing so we don't hold megabytes in memory and JSON
		att.RawData = nil
	}
	return nil
}
