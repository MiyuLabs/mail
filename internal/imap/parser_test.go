package imap

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	goimap "github.com/emersion/go-imap"

	mailpkg "github.com/MiyuLabs/mail/internal/mail"
)

func loadMessageFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

func parseFixture(t *testing.T, name string) (*mailpkg.Message, error) {
	t.Helper()
	return ParseMessage(&goimap.Message{Uid: 42, Flags: []string{goimap.SeenFlag}}, loadMessageFixture(t, name), "example.com")
}

func TestParseMessageFixtures(t *testing.T) {
	tests := []struct {
		name       string
		from       string
		fromName   string
		subject    string
		bodyText   string
		bodyHTML   string
		attachment int
	}{
		{name: "plain_text.eml", from: "alice@example.org", subject: "Plain message", bodyText: "Hello from a plain text message.\n"},
		{name: "html.eml", from: "bob@example.org", subject: "HTML message", bodyHTML: "<html><body><p>Hello <strong>HTML</strong>.</p></body></html>\n"},
		{name: "alternative.eml", from: "casey@example.org", subject: "Alternative message", bodyText: "Read this in plain text.", bodyHTML: "<p>Read this in <b>HTML</b>.</p>"},
		{name: "mixed_attachment.eml", from: "dana@example.org", subject: "Report attached", bodyText: "Please see the report.", attachment: 1},
		{name: "related_inline_image.eml", from: "eli@example.org", subject: "Inline image", bodyHTML: "<p>Logo: <img src=\"cid:logo@example.org\"></p>", attachment: 1},
		{name: "nested_multipart.eml", from: "fran@example.org", subject: "Nested message", bodyText: "Nested plain body.", bodyHTML: "<p>Nested HTML body.</p>", attachment: 1},
		{name: "quoted_printable.eml", from: "grace@example.org", subject: "Quoted printable", bodyText: "André says hello,\nwith a soft line break.\n"},
		{name: "base64_body.eml", from: "kim@example.org", subject: "Base64 body", bodyText: "Base64 body is parsed correctly.\n"},
		{name: "utf8_headers_filename.eml", from: "jose@example.org", subject: "Olá from José", bodyText: "Olá!", attachment: 1},
		{name: "missing_duplicate_headers.eml", from: "first@example.org", subject: "First subject", bodyText: "Duplicate fields use the standard first-value Header.Get behavior.\n"},
		{name: "large_message.eml", from: "ian@example.org", subject: "Large body"},
		{name: "empty_body.eml", from: "jules@example.org", subject: "Empty body"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFixture(t, tt.name)
			if err != nil {
				t.Fatalf("ParseMessage() error = %v", err)
			}
			if got.FromAddress != tt.from || got.FromName != tt.fromName {
				t.Errorf("sender = %q (%q), want %q (%q)", got.FromAddress, got.FromName, tt.from, tt.fromName)
			}
			if tt.name == "missing_duplicate_headers.eml" {
				if len(got.CcAddresses) != 0 || len(got.BccAddresses) != 0 || got.ReplyTo != "" {
					t.Errorf("missing optional headers produced cc=%v bcc=%v reply-to=%q", got.CcAddresses, got.BccAddresses, got.ReplyTo)
				}
				if got.OriginalTo != "support@example.com" {
					t.Errorf("OriginalTo fallback = %q, want support@example.com", got.OriginalTo)
				}
			}
			if got.Subject != tt.subject {
				t.Errorf("subject = %q, want %q", got.Subject, tt.subject)
			}
			if tt.bodyText != "" && got.BodyText != tt.bodyText {
				t.Errorf("text body = %q, want %q", got.BodyText, tt.bodyText)
			}
			if tt.bodyHTML != "" && got.BodyHTML != tt.bodyHTML {
				t.Errorf("HTML body = %q, want %q", got.BodyHTML, tt.bodyHTML)
			}
			if len(got.Attachments) != tt.attachment {
				t.Fatalf("attachments = %d, want %d", len(got.Attachments), tt.attachment)
			}
			if got.HasAttachments != (tt.attachment > 0) {
				t.Errorf("HasAttachments = %v, want %v", got.HasAttachments, tt.attachment > 0)
			}
			if got.IMAPuid != 42 || !got.IsRead {
				t.Errorf("message metadata uid=%d read=%v, want uid=42 read=true", got.IMAPuid, got.IsRead)
			}
		})
	}
}

func TestParseMessageAttachmentMetadataAndBytes(t *testing.T) {
	tests := []struct {
		fixture, filename, contentType, contentID string
		inline                                    bool
		content                                   []byte
	}{
		{"mixed_attachment.eml", "report.csv", "text/csv", "", false, []byte("name,value\ntotal,42")},
		{"related_inline_image.eml", "logo.png", "image/png", "logo@example.org", true, []byte("hello-image")},
		{"nested_multipart.eml", "data.bin", "application/octet-stream", "", false, []byte{0, 1, 2, 255}},
		{"utf8_headers_filename.eml", "café.txt", "text/plain", "", false, []byte("café")},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			got, err := parseFixture(t, tt.fixture)
			if err != nil {
				t.Fatalf("ParseMessage() error = %v", err)
			}
			if len(got.Attachments) != 1 {
				t.Fatalf("attachments = %d, want 1", len(got.Attachments))
			}
			att := got.Attachments[0]
			if att.Filename != tt.filename || att.ContentType != tt.contentType || att.ContentID != tt.contentID || att.IsInline != tt.inline {
				t.Errorf("attachment metadata = %+v, want filename=%q type=%q cid=%q inline=%v", att, tt.filename, tt.contentType, tt.contentID, tt.inline)
			}
			if !bytes.Equal(att.RawData, tt.content) {
				t.Errorf("attachment bytes = %v, want %v", att.RawData, tt.content)
			}
			if att.SizeBytes != int64(len(tt.content)) {
				t.Errorf("attachment size = %d, want %d", att.SizeBytes, len(tt.content))
			}
		})
	}
}

func TestParseMalformedFixturesGracefully(t *testing.T) {
	t.Run("malformed RFC message returns error", func(t *testing.T) {
		got, err := parseFixture(t, "malformed_message.eml")
		if err == nil || got != nil {
			t.Fatalf("ParseMessage() = (%v, %v), want (nil, error)", got, err)
		}
	})

	t.Run("malformed MIME boundary retains message with fallback body", func(t *testing.T) {
		got, err := parseFixture(t, "malformed_boundary.eml")
		if err != nil {
			t.Fatalf("ParseMessage() error = %v, want graceful message return", err)
		}
		if got.Subject != "Broken boundary" || got.BodyText != "[could not parse message body]" {
			t.Errorf("malformed MIME result subject=%q body=%q", got.Subject, got.BodyText)
		}
	})
}

func TestParseLargeAndEmptyBodies(t *testing.T) {
	t.Run("large body is not truncated", func(t *testing.T) {
		got, err := parseFixture(t, "large_message.eml")
		if err != nil {
			t.Fatalf("ParseMessage() error = %v", err)
		}
		if !strings.HasPrefix(got.BodyText, "LARGE_BODY_SENTINEL_BEGIN\n") || !strings.HasSuffix(got.BodyText, "LARGE_BODY_SENTINEL_END\n") {
			t.Errorf("large body lost boundary sentinel(s), length = %d", len(got.BodyText))
		}
		if len(got.BodyText) < 4000 {
			t.Errorf("large body length = %d, want at least 4000", len(got.BodyText))
		}
	})

	t.Run("empty body is safe", func(t *testing.T) {
		got, err := parseFixture(t, "empty_body.eml")
		if err != nil {
			t.Fatalf("ParseMessage() error = %v", err)
		}
		if got.BodyText != "" || got.BodyHTML != "" || got.Snippet != "" {
			t.Errorf("empty body fields = text %q HTML %q snippet %q", got.BodyText, got.BodyHTML, got.Snippet)
		}
	})
}
