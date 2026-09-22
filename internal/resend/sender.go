// Package resend wraps the Resend API for sending outbound emails from
// @domain.tld identities with correct RFC 5322 threading headers.
package resend

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	resendgo "github.com/resend/resend-go/v2"

	mailpkg "github.com/MiyuLabs/mail/internal/mail"
)

// Sender sends email via the Resend API.
type Sender struct {
	client *resendgo.Client
	domain string
}

// NewSender creates a new Resend-backed email sender.
func NewSender(apiKey, domain string) *Sender {
	return &Sender{
		client: resendgo.NewClient(apiKey),
		domain: domain,
	}
}

// Domain returns the domain configured for this sender.
func (s *Sender) Domain() string {
	return s.domain
}

// Send sends an email as described by the ComposeRequest.
//
// Returns (messageID, resendID, error):
//   - messageID is the RFC 5322 Message-ID we generated and injected into the
//     email headers (e.g. "<uuid@mdomain.tld>"). Use this for In-Reply-To /
//     References on subsequent replies — it is what the recipient's mail client
//     and our threading engine will see.
//   - resendID is Resend's internal tracking UUID (e.g. "56761188-..."). Use
//     this only for Resend API lookups / webhook correlation. It is NOT an RFC
//     Message-ID and MUST NOT be used for threading.
//
// Threading note:
//
//	Resend auto-generates a Message-ID (<hash@resend.dev>) if none is provided.
//	That auto-generated value is never returned by the Send API — only by webhooks.
//	To maintain full control over threading we generate our own compliant
//	Message-ID (<uuid@domain>) and inject it via the headers map. Resend honours
//	custom Message-ID headers and passes them through to the SMTP envelope.
func (s *Sender) Send(ctx context.Context, req *mailpkg.ComposeRequest) (messageID, resendID string, err error) {
	if err := req.Validate(); err != nil {
		return "", "", err
	}

	// ── Generate a stable, RFC 5322-compliant Message-ID ─────────────────────
	// Format: <uuid@domain>  — angle brackets are part of the RFC value.
	rawID := uuid.New().String() + "@" + s.domain
	messageID = "<" + rawID + ">"

	params := &resendgo.SendEmailRequest{
		From:    fmt.Sprintf("%s <%s>", req.Identity.DisplayName, req.Identity.Address),
		To:      req.To,
		Subject: req.Subject,
		Text:    req.BodyText,
		Html:    req.BodyHTML,
		ReplyTo: req.Identity.Address,
	}

	// ── Threading + identity headers ──────────────────────────────────────────
	headers := map[string]string{
		// Our own Message-ID — this is what goes in the actual email envelope.
		"Message-ID": messageID,
	}
	if req.InReplyTo != "" {
		// Ensure angle brackets are present (they are part of the header value).
		inReplyTo := req.InReplyTo
		if len(inReplyTo) == 0 || inReplyTo[0] != '<' {
			inReplyTo = "<" + inReplyTo + ">"
		}
		headers["In-Reply-To"] = inReplyTo
	}
	if len(req.References) > 0 {
		refs := ""
		for i, r := range req.References {
			if i > 0 {
				refs += " "
			}
			if len(r) == 0 || r[0] != '<' {
				r = "<" + r + ">"
			}
			refs += r
		}
		headers["References"] = refs
	}
	params.Headers = headers

	// ── Cc / Bcc ──────────────────────────────────────────────────────────────
	if len(req.Cc) > 0 {
		params.Cc = req.Cc
	}
	if len(req.Bcc) > 0 {
		params.Bcc = req.Bcc
	}

	// ── Attachments ───────────────────────────────────────────────────────────
	for _, att := range req.Attachments {
		params.Attachments = append(params.Attachments, &resendgo.Attachment{
			Filename: att.Filename,
			Content:  att.Data,
		})
	}

	sent, err := s.client.Emails.Send(params)
	if err != nil {
		return "", "", fmt.Errorf("resend: send failed: %w", err)
	}

	// sent.Id is Resend's internal tracking UUID — store separately, never use
	// for threading.
	return messageID, sent.Id, nil
}

// QuotaInfo describes the remaining daily send quota.
// Resend does not provide a real-time quota API; we estimate from a
// rolling counter maintained in D1.
type QuotaInfo struct {
	DailySent      int
	DailyLimit     int
	PercentUsed    float64
	IsLimitReached bool
}

// EstimateQuota computes quota usage from sent counts stored in D1.
func EstimateQuota(sentToday, dailyLimit int) QuotaInfo {
	pct := 0.0
	if dailyLimit > 0 {
		pct = float64(sentToday) / float64(dailyLimit) * 100
	}
	return QuotaInfo{
		DailySent:      sentToday,
		DailyLimit:     dailyLimit,
		PercentUsed:    pct,
		IsLimitReached: sentToday >= dailyLimit,
	}
}
