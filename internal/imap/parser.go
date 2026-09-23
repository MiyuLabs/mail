// Package imap — parser.go decodes raw RFC 5322 messages into our internal
// mail.Message type, extracting all threading headers, body parts, and attachments.
package imap

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"

	goimap "github.com/emersion/go-imap"

	mailpkg "github.com/MiyuLabs/mail/internal/mail"
)

// ParseMessage converts a raw go-imap Message into our internal Message model.
// The rawBody is the full RFC 5322 byte stream from BODY.PEEK[].
func ParseMessage(msg *goimap.Message, rawBody []byte, domain string) (*mailpkg.Message, error) {
	parsed, err := mail.ReadMessage(bytes.NewReader(rawBody))
	if err != nil {
		return nil, fmt.Errorf("parser: failed to parse RFC 5322 message: %w", err)
	}

	h := parsed.Header

	// ── Addressing ───────────────────────────────────────────────────────────
	from := parseAddressList(h.Get("From"))
	toList := parseAddressList(h.Get("To"))
	ccList := parseAddressList(h.Get("Cc"))
	bccList := parseAddressList(h.Get("Bcc"))

	var fromAddr, fromName string
	if len(from) > 0 {
		fromAddr, fromName = parseFromString(from[0])
	}

	// ── Threading headers ────────────────────────────────────────────────────
	messageID := cleanMessageID(h.Get("Message-ID"))
	inReplyTo := cleanMessageID(h.Get("In-Reply-To"))
	references := parseReferences(h.Get("References"))

	// ── Original recipient (set by Cloudflare Email Worker) ──────────────────
	originalTo := strings.TrimSpace(h.Get("X-Original-To"))
	if originalTo == "" {
		// Fallback: try To header for @domain addresses.
		for _, addr := range toList {
			if strings.Contains(addr, "@" + domain) {
				originalTo = addr
				break
			}
		}
	}

	// ── Date ─────────────────────────────────────────────────────────────────
	receivedAt := time.Now()
	if msg.Envelope != nil && !msg.Envelope.Date.IsZero() {
		receivedAt = msg.Envelope.Date
	} else if d, err := parseDate(h.Get("Date")); err == nil {
		receivedAt = d
	}

	// ── Subject ───────────────────────────────────────────────────────────────
	subject := decodeHeader(h.Get("Subject"))

	// ── Body + Attachments ────────────────────────────────────────────────────
	bodyText, bodyHTML, attachments, err := parseMIMEBody(parsed)
	if err != nil {
		// Non-fatal: a broken MIME body shouldn't drop the message.
		bodyText = "[could not parse message body]"
	}

	// ── Snippet ───────────────────────────────────────────────────────────────
	snippet := makeSnippet(bodyText, 120)

	m := &mailpkg.Message{
		MessageID:      messageID,
		InReplyTo:      inReplyTo,
		References:     references,
		FromAddress:    fromAddr,
		FromName:       fromName,
		ToAddresses:    toList,
		CcAddresses:    ccList,
		BccAddresses:   bccList,
		ReplyTo:        h.Get("Reply-To"),
		Subject:        subject,
		BodyText:       bodyText,
		BodyHTML:       bodyHTML,
		Snippet:        snippet,
		OriginalTo:     originalTo,
		Direction:      mailpkg.DirectionInbound,
		IMAPuid:        msg.Uid,
		HasAttachments: len(attachments) > 0,
		ReceivedAt:     receivedAt,
		Attachments:    attachments,
	}

	// ── Read flag ─────────────────────────────────────────────────────────────
	for _, flag := range msg.Flags {
		if flag == goimap.SeenFlag {
			m.IsRead = true
			break
		}
	}

	return m, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// MIME body parser
// ──────────────────────────────────────────────────────────────────────────────

func parseMIMEBody(msg *mail.Message) (bodyText, bodyHTML string, attachments []mailpkg.Attachment, err error) {
	contentType := msg.Header.Get("Content-Type")
	mediaType, params, _ := mime.ParseMediaType(contentType)

	switch {
	case strings.HasPrefix(mediaType, "multipart/"):
		bodyText, bodyHTML, attachments, err = parseMultipart(msg.Body, params["boundary"])
	case mediaType == "text/html":
		raw, _ := io.ReadAll(decodeTransferEncoding(msg.Header, msg.Body))
		bodyHTML = string(raw)
	default:
		raw, _ := io.ReadAll(decodeTransferEncoding(msg.Header, msg.Body))
		bodyText = string(raw)
	}
	return
}

func parseMultipart(r io.Reader, boundary string) (bodyText, bodyHTML string, attachments []mailpkg.Attachment, err error) {
	mr := multipart.NewReader(r, boundary)
	for {
		part, partErr := mr.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			err = partErr
			return
		}

		ct := part.Header.Get("Content-Type")
		mediaType, params, _ := mime.ParseMediaType(ct)
		disposition, dispParams, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))

		switch {
		case strings.HasPrefix(mediaType, "multipart/"):
			// Nested multipart (e.g. multipart/alternative inside multipart/mixed).
			t, h, atts, _ := parseMultipart(part, params["boundary"])
			if bodyText == "" {
				bodyText = t
			}
			if bodyHTML == "" {
				bodyHTML = h
			}
			attachments = append(attachments, atts...)

		case mediaType == "text/plain" && disposition != "attachment":
			raw, _ := io.ReadAll(decodeTransferEncoding(part.Header, part))
			bodyText = string(raw)

		case mediaType == "text/html" && disposition != "attachment":
			raw, _ := io.ReadAll(decodeTransferEncoding(part.Header, part))
			bodyHTML = string(raw)

		default:
			// Treat as attachment.
			raw, _ := io.ReadAll(decodeTransferEncoding(part.Header, part))
			filename := dispParams["filename"]
			if filename == "" {
				filename = params["name"]
			}
			if filename == "" {
				filename = "attachment"
			}
			att := mailpkg.Attachment{
				Filename:    decodeHeader(filename),
				ContentType: mediaType,
				SizeBytes:   int64(len(raw)),
				ContentID:   strings.Trim(part.Header.Get("Content-ID"), "<>"),
				IsInline:    disposition == "inline",
				// Raw bytes available for immediate cache write.
				RawData: raw,
			}
			attachments = append(attachments, att)
		}
	}
	return
}

// ──────────────────────────────────────────────────────────────────────────────
// Header helpers
// ──────────────────────────────────────────────────────────────────────────────

// cleanMessageID strips angle brackets and trims whitespace.
func cleanMessageID(raw string) string {
	id := strings.TrimSpace(raw)
	id = strings.TrimPrefix(id, "<")
	id = strings.TrimSuffix(id, ">")
	return id
}

// parseReferences splits the References header into individual Message-IDs.
func parseReferences(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Fields(raw)
	refs := make([]string, 0, len(parts))
	for _, p := range parts {
		if cleaned := cleanMessageID(p); cleaned != "" {
			refs = append(refs, cleaned)
		}
	}
	return refs
}

// parseAddressList parses an RFC 5322 address list header value.
// Returns a slice of "Name <addr>" or "addr" strings.
func parseAddressList(raw string) []string {
	if raw == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(raw)
	if err != nil {
		// Best-effort: return raw string split by comma.
		return strings.Split(raw, ",")
	}
	result := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		result = append(result, addr.Address)
	}
	return result
}

// decodeHeader decodes RFC 2047 encoded-word strings (e.g. =?UTF-8?Q?...?=).
func decodeHeader(raw string) string {
	dec := new(mime.WordDecoder)
	decoded, err := dec.DecodeHeader(raw)
	if err != nil {
		return raw
	}
	return decoded
}

// parseDate parses a Date header value.
func parseDate(raw string) (time.Time, error) {
	return mail.ParseDate(raw)
}

// decodeTransferEncoding wraps a reader to handle quoted-printable or base64 encoding.
func decodeTransferEncoding(header interface{ Get(string) string }, r io.Reader) io.Reader {
	switch strings.ToLower(header.Get("Content-Transfer-Encoding")) {
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, r)
	default:
		return r
	}
}

// makeSnippet creates a short preview string from body text.
func makeSnippet(bodyText string, maxLen int) string {
	text := strings.Join(strings.Fields(bodyText), " ")
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "…"
}

// parseFromString extracts the display name and bare email address from a
// single address string like "Alice <alice@example.com>" or "alice@example.com".
func parseFromString(s string) (addr, name string) {
	parsed, err := mail.ParseAddress(s)
	if err != nil {
		// Fallback: treat the whole string as the address.
		return strings.TrimSpace(s), ""
	}
	return parsed.Address, parsed.Name
}

