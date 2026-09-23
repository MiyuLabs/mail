// Package mail — threading.go implements the Message-ID/In-Reply-To/References
// based threading engine. No subject matching is used as a primary mechanism.
package mail

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ThreadEngine maintains in-memory indexes needed for fast thread assignment.
// The persistent store is D1; this is the hot-path in-process cache.
type ThreadEngine struct {
	// messageByID maps Message-ID → Thread ID.
	messageByID map[string]string
	// threadByID maps Thread ID → Thread.
	threadByID map[string]*Thread

	// Callbacks for database lookup on cache miss
	LookupThreadIDFunc func(messageID string) (string, bool)
	GetThreadFunc      func(threadID string) (*Thread, bool)
}

// NewThreadEngine creates an empty threading engine.
// Call Hydrate() to load existing state from the store.
func NewThreadEngine(lookupFunc func(string) (string, bool), getThreadFunc func(string) (*Thread, bool)) *ThreadEngine {
	return &ThreadEngine{
		messageByID:        make(map[string]string),
		threadByID:         make(map[string]*Thread),
		LookupThreadIDFunc: lookupFunc,
		GetThreadFunc:      getThreadFunc,
	}
}

// Hydrate loads existing message→thread mappings from a slice of messages.
// Call this on startup with all messages loaded from D1.
func (te *ThreadEngine) Hydrate(messages []Message, threads []Thread) {
	for i := range threads {
		t := &threads[i]
		te.threadByID[t.ID] = t
	}
	for _, m := range messages {
		if m.MessageID != "" && m.ThreadID != "" {
			te.messageByID[m.MessageID] = m.ThreadID
		}
	}
}

// AssignThread determines which thread a new message belongs to,
// or creates a new thread if none matches. It updates internal indexes.
// The returned Thread is either existing (updated) or newly created.
//
// Algorithm (RFC 5322 compliant, no subject matching):
//  1. Check In-Reply-To against known Message-IDs.
//  2. Walk References (last → first) for a match.
//  3. If nothing matches: create a new thread.
func (te *ThreadEngine) AssignThread(m *Message, identityID string) (*Thread, error) {
	// 0. Check if this exact message is already in a thread (IMAP duplicate).
	if m.MessageID != "" {
		if threadID, ok := te.LookupThreadID(m.MessageID); ok {
			if t, ok := te.getThread(threadID); ok {
				return t, nil
			}
		}
	}

	// 1. Try In-Reply-To.
	if m.InReplyTo != "" {
		if threadID, ok := te.LookupThreadID(m.InReplyTo); ok {
			return te.appendToThread(threadID, m)
		}
	}

	// 2. Walk References in reverse order (most recent first).
	for i := len(m.References) - 1; i >= 0; i-- {
		ref := m.References[i]
		if threadID, ok := te.LookupThreadID(ref); ok {
			return te.appendToThread(threadID, m)
		}
	}

	// 3. No existing thread — create a new one.
	thread := &Thread{
		ID:            newUUID(),
		Subject:       NormalizeSubject(m.Subject),
		IdentityID:    identityID,
		LastMessageAt:    m.ReceivedAt,
		MessageCount:     1,
		IsRead:           m.IsRead,
		Snippet:          m.Snippet,
		ParticipantNames: getSenderName(m),
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	te.threadByID[thread.ID] = thread
	te.index(m.MessageID, thread.ID)
	return thread, nil
}

// getThread returns the Thread object, fetching from DB if not in memory.
func (te *ThreadEngine) getThread(threadID string) (*Thread, bool) {
	if t, ok := te.threadByID[threadID]; ok {
		return t, true
	}
	if te.GetThreadFunc != nil {
		if t, ok := te.GetThreadFunc(threadID); ok {
			te.threadByID[threadID] = t
			return t, true
		}
	}
	return nil, false
}

// appendToThread updates an existing thread with a new message.
func (te *ThreadEngine) appendToThread(threadID string, m *Message) (*Thread, error) {
	thread, ok := te.getThread(threadID)
	if !ok {
		return nil, fmt.Errorf("threading: thread %s not found in index or db", threadID)
	}
	thread.MessageCount++
	if m.ReceivedAt.After(thread.LastMessageAt) {
		thread.LastMessageAt = m.ReceivedAt
		thread.Snippet = m.Snippet
		
		// Very simple participant names logic: just use the latest sender.
		// For a more complete client, this would combine multiple participants.
		thread.ParticipantNames = getSenderName(m)
	}
	if !m.IsRead {
		thread.IsRead = false
	}
	thread.UpdatedAt = time.Now()
	te.index(m.MessageID, thread.ID)
	return thread, nil
}

// index adds a Message-ID → Thread-ID mapping.
func (te *ThreadEngine) index(messageID, threadID string) {
	if messageID != "" {
		te.messageByID[messageID] = threadID
	}
}

// LookupThreadID returns the thread ID for a given Message-ID, if known.
func (te *ThreadEngine) LookupThreadID(messageID string) (string, bool) {
	if id, ok := te.messageByID[messageID]; ok {
		return id, true
	}
	if te.LookupThreadIDFunc != nil {
		id, ok := te.LookupThreadIDFunc(messageID)
		if ok {
			te.messageByID[messageID] = id
			return id, true
		}
	}
	return "", false
}

// ──────────────────────────────────────────────────────────────────────────────
// Composer helpers (reply header construction)
// ──────────────────────────────────────────────────────────────────────────────

// BuildReplyHeaders constructs the RFC 5322 threading headers for a reply.
// Returns In-Reply-To and References values ready for use in Resend API call.
func BuildReplyHeaders(parent *Message) (inReplyTo, references string) {
	inReplyTo = "<" + parent.MessageID + ">"

	refs := parent.BuildReplyReferences()
	parts := make([]string, len(refs))
	for i, r := range refs {
		parts[i] = "<" + r + ">"
	}
	references = strings.Join(parts, " ")
	return
}

func getSenderName(m *Message) string {
	// For outbound emails starting a thread, show who it was sent to
	if m.Direction == DirectionOutbound && len(m.ToAddresses) > 0 {
		parts := strings.Split(m.ToAddresses[0], "@")
		if len(parts) > 0 {
			return "To: " + parts[0]
		}
		return "To: " + m.ToAddresses[0]
	}

	if m.FromName != "" {
		return m.FromName
	}
	parts := strings.Split(m.FromAddress, "@")
	if len(parts) > 0 {
		return parts[0]
	}
	return m.FromAddress
}

// ──────────────────────────────────────────────────────────────────────────────
// Utilities
// ──────────────────────────────────────────────────────────────────────────────

func newUUID() string {
	return uuid.New().String()
}
