// Package mail defines the core domain types for the mail client.
// These are the canonical models shared across IMAP, D1, Resend, and UI layers.
package mail

import (
	"time"
)

// Direction indicates whether a message was received or sent.
type Direction string

const (
	DirectionInbound  Direction = "inbound"
	DirectionOutbound Direction = "outbound"
)

// IdentityType indicates whether an identity receives mail (mailbox) or is send-only.
type IdentityType string

const (
	IdentityTypeMailbox  IdentityType = "mailbox"
	IdentityTypeSendOnly IdentityType = "send_only"
)

// Identity represents a virtual @domain.tld email address.
type Identity struct {
	ID          string       `json:"id"`
	Address     string       `json:"address"`      // e.g. "careers@domain.tld"
	DisplayName string       `json:"display_name"` // e.g. "Org. Careers"
	Type        IdentityType `json:"identity_type"`
	IsActive    bool         `json:"is_active"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// IsSendOnly returns true for send-only identities (e.g. noreply@).
func (i Identity) IsSendOnly() bool {
	return i.Type == IdentityTypeSendOnly
}

// Thread is a conversation — our own threading model, independent of Gmail's.
type Thread struct {
	ID            string    `json:"id"`
	Subject       string    `json:"subject"`       // normalized (no Re:/Fwd: prefix)
	IdentityID    string    `json:"identity_id"`   // which mailbox received this
	LastMessageAt time.Time `json:"last_message_at"`
	MessageCount  int       `json:"message_count"`
	IsRead        bool      `json:"is_read"`
	IsStarred     bool      `json:"is_starred"`
	IsArchived    bool      `json:"is_archived"`
	IsTrashed        bool      `json:"is_trashed"`
	Snippet          string    `json:"snippet"`
	ParticipantNames string    `json:"participant_names"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`

	// Populated in-memory (not stored in D1) for convenience.
	Messages []Message `json:"messages,omitempty"`
	Identity *Identity `json:"identity,omitempty"`
}

// Message is a single email within a thread.
type Message struct {
	// Internal IDs.
	ID       string `json:"id"`        // our UUID
	ThreadID string `json:"thread_id"`

	// RFC 5322 threading headers.
	MessageID  string   `json:"message_id"`  // RFC 5322 Message-ID (without angle brackets)
	InReplyTo  string   `json:"in_reply_to"` // parent's Message-ID
	References []string `json:"references"`  // ancestor chain of Message-IDs

	// Addressing.
	FromAddress  string   `json:"from_address"`
	FromName     string   `json:"from_name"`
	ToAddresses  []string `json:"to_addresses"`
	CcAddresses  []string `json:"cc_addresses"`
	BccAddresses []string `json:"bcc_addresses"`
	ReplyTo      string   `json:"reply_to"`

	// Content.
	Subject  string `json:"subject"`
	BodyText string `json:"body_text"`
	BodyHTML string `json:"body_html"`
	Snippet  string `json:"snippet"`

	// Routing metadata.
	// X-Original-To header injected by the Cloudflare Email Worker.
	// Maps to the @domain.tld address the sender addressed.
	OriginalTo string    `json:"original_to"`
	Direction  Direction `json:"direction"`

	// External references.
	IMAPuid  uint32 `json:"imap_uid,omitempty"` // Gmail IMAP UID
	ResendID string `json:"resend_id,omitempty"` // Resend API email ID

	// State.
	IsRead         bool `json:"is_read"`
	IsDraft        bool `json:"is_draft"`
	HasAttachments bool `json:"has_attachments"`

	// Timestamps.
	ReceivedAt time.Time `json:"received_at"`
	CreatedAt  time.Time `json:"created_at"`

	// Populated in-memory (not stored in D1 main row).
	Attachments []Attachment `json:"attachments,omitempty"`
}

// ReferencesString returns the References header value as a space-separated string.
func (m *Message) ReferencesString() string {
	refs := make([]string, len(m.References))
	for i, r := range m.References {
		refs[i] = "<" + r + ">"
	}
	s := ""
	for i, r := range refs {
		if i > 0 {
			s += " "
		}
		s += r
	}
	return s
}

// BuildReplyReferences constructs the References header value for a reply to this message.
// Appends this message's Message-ID to its references chain, trimming if > 20 entries.
func (m *Message) BuildReplyReferences() []string {
	refs := append([]string{}, m.References...)
	refs = append(refs, m.MessageID)
	// Trim to keep first + last 19 if chain is too long.
	if len(refs) > 20 {
		refs = append(refs[:1], refs[len(refs)-19:]...)
	}
	return refs
}

// Attachment holds metadata about an email attachment.
// Binary content is stored in local file cache, not in D1.
type Attachment struct {
	ID          string `json:"id"`
	MessageID   string `json:"message_id"` // references messages.id (our UUID)
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentID   string `json:"content_id"`  // for inline images (CID)
	IsInline    bool   `json:"is_inline"`
	StorageKey  string `json:"storage_key"` // local cache path or empty

	// In-memory only: raw bytes when freshly parsed from IMAP.
	RawData []byte `json:"-"`
}

// Draft is a message being composed. Stored in D1 for cross-client sync.
type Draft struct {
	ID           string    `json:"id"`
	ThreadID     string    `json:"thread_id,omitempty"` // empty for new compose
	IdentityID   string    `json:"identity_id"`
	ToAddresses  []string  `json:"to_addresses"`
	CcAddresses  []string  `json:"cc_addresses"`
	BccAddresses []string  `json:"bcc_addresses"`
	Subject      string    `json:"subject"`
	BodyText     string    `json:"body_text"`
	BodyHTML     string    `json:"body_html"`
	InReplyTo    string    `json:"in_reply_to"`  // Message-ID if this is a reply
	References   []string  `json:"references"`
	IsReply      bool      `json:"is_reply"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Participant is a contact seen in message addressing. Used for autocomplete.
type Participant struct {
	ID           string    `json:"id"`
	Address      string    `json:"address"`
	DisplayName  string    `json:"display_name"`
	LastSeenAt   time.Time `json:"last_seen_at"`
	MessageCount int       `json:"message_count"`
}

// SyncCursor tracks per-client sync state.
type SyncCursor struct {
	ClientID  string    `json:"client_id"`
	IMAPuid   uint32    `json:"imap_uid"`   // last successfully synced IMAP UID
	D1Cursor  string    `json:"d1_cursor"`  // ISO timestamp of last D1 change pulled
	UpdatedAt time.Time `json:"updated_at"`
}

// NormalizeSubject strips Re:/Fwd: prefixes for thread grouping.
func NormalizeSubject(subject string) string {
	s := subject
	for {
		lower := toLower(s)
		switch {
		case hasPrefix(lower, "re: "):
			s = s[4:]
		case hasPrefix(lower, "fwd: "):
			s = s[5:]
		case hasPrefix(lower, "fw: "):
			s = s[4:]
		default:
			return trimSpace(s)
		}
	}
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		} else {
			b[i] = c
		}
	}
	return string(b)
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
