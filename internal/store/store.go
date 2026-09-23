package store

import (
	"context"
	"os"
	"path/filepath"

	"github.com/MiyuLabs/mail/internal/d1"
	"github.com/MiyuLabs/mail/internal/localdb"
	mailpkg "github.com/MiyuLabs/mail/internal/mail"
)

// IMAPMutator allows the store to push flag changes back to the IMAP server.
type IMAPMutator interface {
	SetFlagged(ctx context.Context, uids []uint32, flagged bool) error
	Archive(ctx context.Context, uids []uint32) error
	Trash(ctx context.Context, uids []uint32) error
}

// Store coordinates data flow between the UI, the local SQLite cache, and D1.
type Store struct {
	local       *localdb.Client
	remote      *d1.Client
	imapMutator IMAPMutator
	cacheDir    string
}

// NewStore creates a new coordinator.
func NewStore(local *localdb.Client, remote *d1.Client, imapMutator IMAPMutator, cacheDir string) *Store {
	return &Store{
		local:       local,
		remote:      remote,
		imapMutator: imapMutator,
		cacheDir:    cacheDir,
	}
}

// Local returns the underlying localdb client, mostly used by SyncEngine
// to populate the cache during sync.
func (s *Store) Local() *localdb.Client {
	return s.local
}

// Remote returns the underlying D1 client, mostly used by SyncEngine.
func (s *Store) Remote() *d1.Client {
	return s.remote
}

// ListIdentities reads identities from the local cache.
func (s *Store) ListIdentities(ctx context.Context) ([]mailpkg.Identity, error) {
	return s.local.ListIdentities(ctx)
}

// ListThreads reads a page of threads from the local cache.
func (s *Store) ListThreads(ctx context.Context, req mailpkg.PageRequest) (*mailpkg.ThreadPage, error) {
	return s.local.ListThreads(ctx, req)
}

// SearchParticipants searches participants by prefix.
func (s *Store) SearchParticipants(ctx context.Context, prefix string) ([]string, error) {
	return s.local.SearchParticipants(ctx, prefix)
}

// SearchThreads searches threads by a query string.
func (s *Store) SearchThreads(ctx context.Context, req mailpkg.PageRequest, query string) (*mailpkg.ThreadPage, error) {
	return s.local.SearchThreads(ctx, req, query)
}

// GetThreadIDByMessageID looks up a thread ID from a message ID.
func (s *Store) GetThreadIDByMessageID(ctx context.Context, messageID string) (string, error) {
	return s.local.GetThreadIDByMessageID(ctx, messageID)
}

// GetThread reads a complete thread and its messages from the local cache.
func (s *Store) GetThread(ctx context.Context, threadID string) (*mailpkg.Thread, error) {
	t, err := s.local.GetThread(ctx, threadID)
	if err != nil || t == nil {
		return t, err
	}
	
	// Populate attachment metadata from disk
	for i := range t.Messages {
		if t.Messages[i].HasAttachments {
			dir := filepath.Join(s.cacheDir, "attachments", t.Messages[i].MessageID)
			entries, _ := os.ReadDir(dir)
			for _, entry := range entries {
				if !entry.IsDir() {
					info, err := entry.Info()
					if err == nil {
						t.Messages[i].Attachments = append(t.Messages[i].Attachments, mailpkg.Attachment{
							Filename:   entry.Name(),
							SizeBytes:  info.Size(),
							StorageKey: filepath.Join(dir, entry.Name()),
						})
					}
				}
			}
		}
	}
	
	return t, nil
}

// QueueOutbox queues an email to be sent in the background.
func (s *Store) QueueOutbox(ctx context.Context, id, messageID, identityID, payload string) error {
	return s.local.QueueOutbox(ctx, id, messageID, identityID, payload)
}

// MarkThreadArchived updates the thread state locally and remotely.
func (s *Store) MarkThreadArchived(ctx context.Context, threadID string) error {
	if err := s.local.MarkThreadArchived(ctx, threadID); err != nil {
		return err
	}
	
	// Push to IMAP asynchronously if available
	if s.imapMutator != nil {
		go func() {
			if uids := s.getThreadUIDs(context.Background(), threadID); len(uids) > 0 {
				_ = s.imapMutator.Archive(context.Background(), uids)
			}
		}()
	}

	// Fire-and-forget to D1
	thread, err := s.local.GetThread(ctx, threadID)
	if err == nil {
		go func() {
			_ = s.remote.UpsertThread(context.Background(), thread)
		}()
	}
	return nil
}

// MarkThreadRead updates the thread state locally and remotely.
func (s *Store) MarkThreadRead(ctx context.Context, threadID string) error {
	if err := s.local.MarkThreadRead(ctx, threadID); err != nil {
		return err
	}
	thread, err := s.local.GetThread(ctx, threadID)
	if err != nil {
		return err
	}
	return s.remote.UpsertThread(ctx, thread)
}

// MarkThreadStarred updates the thread state locally and remotely.
func (s *Store) MarkThreadStarred(ctx context.Context, threadID string, starred bool) error {
	if err := s.local.MarkThreadStarred(ctx, threadID, starred); err != nil {
		return err
	}
	
	// Push to IMAP asynchronously if available
	if s.imapMutator != nil {
		go func() {
			if uids := s.getThreadUIDs(context.Background(), threadID); len(uids) > 0 {
				_ = s.imapMutator.SetFlagged(context.Background(), uids, starred)
			}
		}()
	}

	thread, err := s.local.GetThread(ctx, threadID)
	if err == nil {
		go func() {
			_ = s.remote.UpsertThread(context.Background(), thread)
		}()
	}
	return nil
}

// MarkThreadTrashed updates the thread state locally and remotely.
func (s *Store) MarkThreadTrashed(ctx context.Context, threadID string) error {
	if err := s.local.MarkThreadTrashed(ctx, threadID); err != nil {
		return err
	}
	
	// Push to IMAP asynchronously if available
	if s.imapMutator != nil {
		go func() {
			if uids := s.getThreadUIDs(context.Background(), threadID); len(uids) > 0 {
				_ = s.imapMutator.Trash(context.Background(), uids)
			}
		}()
	}

	thread, err := s.local.GetThread(ctx, threadID)
	if err == nil {
		go func() {
			_ = s.remote.UpsertThread(context.Background(), thread)
		}()
	}
	return nil
}

// getThreadUIDs is a helper to fetch IMAP UIDs for a thread's messages
func (s *Store) getThreadUIDs(ctx context.Context, threadID string) []uint32 {
	t, err := s.local.GetThread(ctx, threadID)
	if err != nil || t == nil {
		return nil
	}
	var uids []uint32
	for _, m := range t.Messages {
		if m.IMAPuid > 0 {
			uids = append(uids, uint32(m.IMAPuid))
		}
	}
	return uids
}

// SaveDraft saves a draft message locally and remotely.
func (s *Store) SaveDraft(ctx context.Context, draft *mailpkg.Draft) error {
	// TODO: implement local draft saving
	return s.remote.SaveDraft(ctx, draft)
}

// RecordSent saves a sent message locally and remotely.
func (s *Store) RecordSent(ctx context.Context, msg *mailpkg.Message) error {
	if len(msg.Attachments) > 0 {
		_ = s.CacheAttachments(msg)
	}
	if err := s.local.UpsertMessage(ctx, msg); err != nil {
		return err
	}
	return s.remote.RecordSent(ctx, msg)
}

// CacheAttachments writes attachment binary data to local disk.
func (s *Store) CacheAttachments(m *mailpkg.Message) error {
	for i := range m.Attachments {
		att := &m.Attachments[i]
		if len(att.RawData) == 0 {
			continue
		}
		dir := filepath.Join(s.cacheDir, "attachments", m.MessageID)
		
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		
		path := filepath.Join(dir, att.Filename)
		if err := os.WriteFile(path, att.RawData, 0644); err != nil {
			return err
		}
		att.RawData = nil // clear memory
	}
	return nil
}
