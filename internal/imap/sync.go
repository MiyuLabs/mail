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
	imap           *Client
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
	imapClient *Client,
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
//  2. Fetch and fully parse the latest 30 new messages.
//  3. Kick off a background goroutine to sync older headers.
func (se *SyncEngine) InitialSync(ctx context.Context) error {
	// 1. Get last known state from D1.
	pullResp, err := se.store.Remote().Pull(ctx, d1.PullRequest{ClientID: se.clientID})
	if err != nil {
		log.Printf("sync: D1 pull failed (continuing offline): %v", err)
	} else {
		se.lastUID = pullResp.LastIMAPUID
		// Hydrate the threading engine with existing data.
		se.threadEngine.Hydrate(pullResp.Messages, pullResp.Threads)

		// Save pulled state to local cache.
		for _, t := range pullResp.Threads {
			// Create a copy for the pointer
			tCopy := t
			_ = se.store.Local().UpsertThread(ctx, &tCopy)
		}
		for _, m := range pullResp.Messages {
			mCopy := m
			_ = se.store.Local().UpsertMessage(ctx, &mCopy)
		}
	}

	// 2. Fetch new UIDs from IMAP.
	if _, err := se.imap.SelectInbox(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	newUIDs, err := se.imap.SearchNewUIDs(se.lastUID)
	if err != nil {
		return fmt.Errorf("sync: search new UIDs: %w", err)
	}

	if len(newUIDs) == 0 {
		log.Println("sync: no new messages")
		return nil
	}

	log.Printf("sync: %d new messages to fetch", len(newUIDs))

	// Fetch most recent first (UIDs are ascending; take last N).
	fullFetchUIDs := newUIDs
	if len(fullFetchUIDs) > initialFullFetchCount {
		fullFetchUIDs = newUIDs[len(newUIDs)-initialFullFetchCount:]
	}

	if err := se.fetchAndProcess(ctx, fullFetchUIDs, true); err != nil {
		return err
	}

	// 3. Background sync for older messages (headers only).
	if len(newUIDs) > initialFullFetchCount {
		olderUIDs := newUIDs[:len(newUIDs)-initialFullFetchCount]
		go func() {
			bgCtx := context.Background()
			if err := se.fetchAndProcess(bgCtx, olderUIDs, false); err != nil {
				log.Printf("sync: background header sync error: %v", err)
			}
		}()
	}

	return nil
}

// SyncNewMessages fetches messages newer than the current lastUID.
// Called after an IMAP IDLE EXISTS notification.
func (se *SyncEngine) SyncNewMessages(ctx context.Context) error {
	if _, err := se.imap.SelectInbox(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	newUIDs, err := se.imap.SearchNewUIDs(se.lastUID)
	if err != nil {
		return fmt.Errorf("sync: search new UIDs: %w", err)
	}

	if len(newUIDs) == 0 {
		return nil
	}

	log.Printf("sync: %d new messages (post-IDLE)", len(newUIDs))
	return se.fetchAndProcess(ctx, newUIDs, true)
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
	return se.processRawMessage(ctx, raw)
}

// ──────────────────────────────────────────────────────────────────────────────
// internal
// ──────────────────────────────────────────────────────────────────────────────

func (se *SyncEngine) fetchAndProcess(ctx context.Context, uids []uint32, full bool) error {
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

	for raw := range msgCh {
		m, err := se.processRawMessage(ctx, raw)
		if err != nil {
			log.Printf("sync: skipping UID %d: %v", raw.Uid, err)
			// Still advance lastUID so we don't retry this message forever.
			if raw.Uid > se.lastUID {
				se.lastUID = raw.Uid
				toSync.LastIMAPUID = se.lastUID
			}
			continue
		}
		if m == nil {
			// Filtered (not Cloudflare-routed) — advance cursor, don't index.
			if raw.Uid > se.lastUID {
				se.lastUID = raw.Uid
				toSync.LastIMAPUID = se.lastUID
			}
			continue
		}

		if raw.Uid > se.lastUID {
			se.lastUID = raw.Uid
		}

		toSync.Messages = append(toSync.Messages, *m)
		toSync.LastIMAPUID = se.lastUID
	}

	if fetchErr != nil {
		return fmt.Errorf("sync: IMAP fetch error: %w", fetchErr)
	}

	// Push all processed messages to D1 and localdb in one batch.
	if len(toSync.Messages) > 0 {
		for _, m := range toSync.Messages {
			mCopy := m
			if err := se.store.Local().UpsertMessage(ctx, &mCopy); err != nil {
				log.Printf("sync: localdb message upsert failed: %v", err)
			}
		}

		if err := se.store.Remote().Push(ctx, toSync); err != nil {
			log.Printf("sync: D1 push failed (data will be retried): %v", err)
			// Non-fatal: messages are visible locally even if D1 sync failed.
		}
	}

	return nil
}

func (se *SyncEngine) processRawMessage(ctx context.Context, raw *goimap.Message) (*mailpkg.Message, error) {
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

	// Resolve identity.
	identity, matched := se.resolver.Resolve(m)

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

	// Assign to thread.
	thread, err := se.threadEngine.AssignThread(m, identityID)
	if err != nil {
		log.Printf("sync: threading error for %s: %v", m.MessageID, err)
		// Create a new thread as fallback.
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

	// Cache attachments to local disk.
	if len(m.Attachments) > 0 {
		if err := se.cacheAttachments(m); err != nil {
			log.Printf("sync: attachment cache error for %s: %v", m.MessageID, err)
		}
	}

	// Push thread metadata to D1 and localdb.
	if err := se.store.Remote().UpsertThread(ctx, thread); err != nil {
		log.Printf("sync: D1 thread upsert failed: %v", err)
	}
	if err := se.store.Local().UpsertThread(ctx, thread); err != nil {
		log.Printf("sync: localdb thread upsert failed: %v", err)
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
