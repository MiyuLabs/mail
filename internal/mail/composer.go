// Package mail — composer.go builds outbound messages with correct
// RFC 5322 threading headers and validates send preconditions.
package mail

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ComposeRequest holds everything needed to send a new email or reply.
type ComposeRequest struct {
	Identity     *Identity // sending identity (From address)
	To           []string  // recipient addresses
	Cc           []string
	Bcc          []string
	Subject      string
	BodyText     string
	BodyHTML     string
	Attachments  []OutboundAttachment

	// Threading (set automatically for replies; empty for new compose).
	InReplyTo  string   // Message-ID of the parent message
	References []string // ancestor chain from parent.BuildReplyReferences()
	ThreadID   string   // our internal thread ID (for D1 recording)
}

// OutboundAttachment is an attachment to include in an outbound email.
type OutboundAttachment struct {
	Filename    string
	ContentType string
	Data        []byte // raw bytes
}

// BuildReply creates a ComposeRequest pre-filled with correct threading headers
// for a reply to the given message. The caller should set BodyText/BodyHTML.
func BuildReply(parent *Message, replyIdentity *Identity) (*ComposeRequest, error) {
	if replyIdentity == nil {
		return nil, fmt.Errorf("composer: identity is required for reply")
	}
	if replyIdentity.IsSendOnly() {
		return nil, fmt.Errorf("composer: identity %s is send-only and cannot receive replies", replyIdentity.Address)
	}

	inReplyTo, references := BuildReplyHeaders(parent)

	// Determine reply-to address: prefer the message's Reply-To header,
	// fall back to the From address.
	replyTo := parent.FromAddress
	if parent.ReplyTo != "" {
		replyTo = parent.ReplyTo
	}

	subject := parent.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	// Strip angle brackets from inReplyTo/references for internal storage.
	return &ComposeRequest{
		Identity:   replyIdentity,
		To:         []string{replyTo},
		Subject:    subject,
		InReplyTo:  strings.Trim(inReplyTo, "<>"),
		References: parseRefList(references),
		ThreadID:   parent.ThreadID,
	}, nil
}

// BuildForward creates a ComposeRequest pre-filled for forwarding a message.
func BuildForward(parent *Message, fromIdentity *Identity) (*ComposeRequest, error) {
	if fromIdentity == nil {
		return nil, fmt.Errorf("composer: identity is required for forward")
	}
	subject := parent.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "fwd:") {
		subject = "Fwd: " + subject
	}
	// Forwards start a new thread (no In-Reply-To).
	return &ComposeRequest{
		Identity: fromIdentity,
		Subject:  subject,
		BodyText: buildForwardBody(parent),
		BodyHTML: buildForwardBodyHTML(parent),
	}, nil
}

// Validate checks that the ComposeRequest has the minimum required fields.
func (r *ComposeRequest) Validate() error {
	if r.Identity == nil {
		return fmt.Errorf("compose: From identity is required")
	}
	if len(r.To) == 0 {
		return fmt.Errorf("compose: at least one recipient is required")
	}
	if r.Subject == "" {
		return fmt.Errorf("compose: subject is required")
	}
	if r.BodyText == "" && r.BodyHTML == "" {
		return fmt.Errorf("compose: body is required")
	}
	return nil
}

// ToMessage converts a sent ComposeRequest into a Message for D1 recording.
//
//   - messageID is the RFC 5322 Message-ID we generated and injected into the
//     email (e.g. "<uuid@domain.tld>"). This is the canonical threading ID.
//   - resendID is Resend's internal tracking UUID. Stored for API/webhook use only.
func (r *ComposeRequest) ToMessage(messageID, resendID string) *Message {
	return &Message{
		ID:           uuid.New().String(),
		ThreadID:     r.ThreadID,
		MessageID:    messageID, // RFC 5322 ID — used for In-Reply-To on future replies
		InReplyTo:    r.InReplyTo,
		References:   r.References,
		FromAddress:  r.Identity.Address,
		FromName:     r.Identity.DisplayName,
		ToAddresses:  r.To,
		CcAddresses:  r.Cc,
		BccAddresses: r.Bcc,
		Subject:      r.Subject,
		BodyText:     r.BodyText,
		BodyHTML:     r.BodyHTML,
		Snippet:      makeSnippet(r.BodyText, 120),
		OriginalTo:   r.Identity.Address,
		Direction:    DirectionOutbound,
		ResendID:     resendID, // Resend internal UUID — NOT used for threading
		IsRead:       true,
		ReceivedAt:   time.Now(),
		CreatedAt:    time.Now(),
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────────────────────────

func parseRefList(refs string) []string {
	parts := strings.Fields(refs)
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.Trim(p, "<>")
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func makeSnippet(text string, max int) string {
	s := strings.Join(strings.Fields(text), " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func buildForwardBody(parent *Message) string {
	return fmt.Sprintf(
		"\n\n---------- Forwarded message ----------\nFrom: %s\nDate: %s\nSubject: %s\n\n%s",
		parent.FromAddress,
		parent.ReceivedAt.Format("Mon, 2 Jan 2006 15:04:05 -0700"),
		parent.Subject,
		parent.BodyText,
	)
}

func buildForwardBodyHTML(parent *Message) string {
	if parent.BodyHTML == "" {
		return ""
	}
	return fmt.Sprintf(
		`<br><br><div style="border-left:2px solid #ccc;padding-left:8px;color:#666">
<b>---------- Forwarded message ----------</b><br>
<b>From:</b> %s<br>
<b>Date:</b> %s<br>
<b>Subject:</b> %s<br><br>
%s
</div>`,
		parent.FromAddress,
		parent.ReceivedAt.Format("Mon, 2 Jan 2006 15:04:05 -0700"),
		parent.Subject,
		parent.BodyHTML,
	)
}
