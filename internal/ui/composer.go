// Package ui — composer.go is the compose/reply window.
// Handles new compose and reply flows with identity selection,
// recipient entry, subject, HTML/text body, and attachments.
package ui

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	mailpkg "github.com/MiyuLabs/mail/internal/mail"
)

// ComposeWindow is a floating compose/reply window.
type ComposeWindow struct {
	app        *App
	window     fyne.Window
	replyTo    *mailpkg.Message // nil for new compose
	identityID string

	// Form fields.
	identitySelect *widget.Select
	toEntry        *widget.SelectEntry
	ccEntry        *widget.SelectEntry
	subjectEntry   *widget.Entry
	bodyEntry      *widget.Entry
	attachments    []mailpkg.OutboundAttachment
}

// NewComposeWindow creates a new compose window.
// replyTo = nil for new compose; otherwise a reply pre-fills headers.
// identityID is ignored when replyTo is set (identity comes from the message).
func NewComposeWindow(a *App, replyTo *mailpkg.Message, identityID string) *ComposeWindow {
	c := &ComposeWindow{
		app:        a,
		replyTo:    replyTo,
		identityID: identityID,
	}
	return c
}

// Show displays the compose window.
func (c *ComposeWindow) Show() {
	c.window = c.app.fyneApp.NewWindow("Compose")
	c.window.Resize(fyne.NewSize(700, 520))
	c.window.SetTitle("Compose — mail")

	// ── Identity selector ─────────────────────────────────────────────────────
	identityOptions := make([]string, 0, len(c.app.state.Identities))
	identityByOption := make(map[string]*mailpkg.Identity)
	defaultSelection := ""

	for i := range c.app.state.Identities {
		id := &c.app.state.Identities[i]
		label := id.DisplayName + " <" + id.Address + ">"
		identityOptions = append(identityOptions, label)
		identityByOption[label] = id
		if id.ID == c.identityID || (defaultSelection == "" && !id.IsSendOnly()) {
			defaultSelection = label
		}
	}

	c.identitySelect = widget.NewSelect(identityOptions, func(_ string) {})
	c.identitySelect.SetSelected(defaultSelection)

	// ── Recipient fields ──────────────────────────────────────────────────────
	c.toEntry = widget.NewSelectEntry(nil)
	c.toEntry.SetPlaceHolder("To")
	c.toEntry.OnChanged = func(q string) {
		if len(q) < 2 {
			c.toEntry.SetOptions(nil)
			return
		}
		// simple autocomplete
		// wait, query could be multiple emails separated by comma
		parts := strings.Split(q, ",")
		lastPart := strings.TrimSpace(parts[len(parts)-1])
		
		results, _ := c.app.store.SearchParticipants(context.Background(), lastPart)
		if len(results) > 0 {
			c.toEntry.SetOptions(results)
		}
	}

	c.ccEntry = widget.NewSelectEntry(nil)
	c.ccEntry.SetPlaceHolder("Cc (optional)")

	// ── Subject ───────────────────────────────────────────────────────────────
	c.subjectEntry = widget.NewEntry()
	c.subjectEntry.SetPlaceHolder("Subject")

	// ── Body ─────────────────────────────────────────────────────────────────
	c.bodyEntry = widget.NewMultiLineEntry()
	c.bodyEntry.SetPlaceHolder("Write your message here…")
	c.bodyEntry.SetMinRowsVisible(12)
	c.bodyEntry.Wrapping = fyne.TextWrapWord

	// ── Pre-fill for replies ──────────────────────────────────────────────────
	if c.replyTo != nil {
		c.prefillReply()
	}

	// ── Attachment button ─────────────────────────────────────────────────────
	attachLabel := widget.NewLabel("No attachments")
	attachBtn := widget.NewButtonWithIcon("Attach file", theme.FolderOpenIcon(), func() {
		d := dialog.NewFileOpen(func(f fyne.URIReadCloser, err error) {
			if err != nil || f == nil {
				return
			}
			data, err := os.ReadFile(f.URI().Path())
			if err != nil {
				dialog.ShowError(err, c.window)
				return
			}
			att := mailpkg.OutboundAttachment{
				Filename: f.URI().Name(),
				Data:     data,
			}
			c.attachments = append(c.attachments, att)
			names := make([]string, len(c.attachments))
			for i, a := range c.attachments {
				names[i] = a.Filename
			}
			attachLabel.SetText("📎 " + strings.Join(names, ", "))
		}, c.window)
		d.Show()
	})
	attachBtn.Importance = widget.LowImportance

	// ── Send / Discard buttons ────────────────────────────────────────────────
	sendBtn := widget.NewButtonWithIcon("Send", theme.MailSendIcon(), func() {
		c.send(identityByOption)
	})
	sendBtn.Importance = widget.HighImportance

	saveDraftBtn := widget.NewButtonWithIcon("Save draft", theme.DocumentSaveIcon(), func() {
		c.saveDraft(identityByOption)
	})
	saveDraftBtn.Importance = widget.LowImportance

	discardBtn := widget.NewButton("Discard", func() {
		c.window.Close()
	})
	discardBtn.Importance = widget.DangerImportance

	actionRow := container.NewHBox(sendBtn, saveDraftBtn, discardBtn)

	// ── Form layout ───────────────────────────────────────────────────────────
	form := container.NewVBox(
		c.identitySelect,
		widget.NewSeparator(),
		c.toEntry,
		c.ccEntry,
		c.subjectEntry,
		widget.NewSeparator(),
		c.bodyEntry,
		widget.NewSeparator(),
		container.NewHBox(attachBtn, attachLabel),
		actionRow,
	)

	c.window.SetContent(container.NewScroll(form))
	c.window.Show()
}

func (c *ComposeWindow) prefillReply() {
	msg := c.replyTo

	// From identity: use original_to of the message we're replying to.
	for i := range c.app.state.Identities {
		id := &c.app.state.Identities[i]
		if strings.EqualFold(id.Address, msg.OriginalTo) {
			c.identityID = id.ID
			break
		}
	}

	// To: reply to the sender (or Reply-To if set).
	replyAddr := msg.FromAddress
	if msg.ReplyTo != "" {
		replyAddr = msg.ReplyTo
	}
	c.toEntry.SetText(replyAddr)

	// Subject.
	subj := msg.Subject
	if !strings.HasPrefix(strings.ToLower(subj), "re:") {
		subj = "Re: " + subj
	}
	c.subjectEntry.SetText(subj)
}

func (c *ComposeWindow) send(identityByOption map[string]*mailpkg.Identity) {
	selected := c.identitySelect.Selected
	identity, ok := identityByOption[selected]
	if !ok || identity == nil {
		dialog.ShowError(errNoIdentity, c.window)
		return
	}

	req := &mailpkg.ComposeRequest{
		Identity:    identity,
		To:          splitAddresses(c.toEntry.Text),
		Cc:          splitAddresses(c.ccEntry.Text),
		Subject:     c.subjectEntry.Text,
		BodyText:    c.bodyEntry.Text,
		Attachments: c.attachments,
	}

	// Apply threading headers for replies.
	if c.replyTo != nil {
		req.InReplyTo = c.replyTo.MessageID
		req.References = c.replyTo.BuildReplyReferences()
		req.ThreadID = c.replyTo.ThreadID
	}

	if err := req.Validate(); err != nil {
		dialog.ShowError(err, c.window)
		return
	}

	// Queue asynchronously.
	go func() {
		// Create a stable message ID for threading
		rawID := uuid.New().String() + "@" + c.app.sender.Domain()
		messageID := "<" + rawID + ">"

		reqJSON, err := json.Marshal(req)
		if err != nil {
			log.Printf("compose: serialize failed: %v", err)
			return
		}

		outboxID := uuid.New().String()
		if err := c.app.store.QueueOutbox(context.Background(), outboxID, messageID, identity.ID, string(reqJSON)); err != nil {
			log.Printf("compose: queue failed: %v", err)
			fyne.Do(func() {
				dialog.ShowError(err, c.window)
			})
			return
		}

		// Message is queued, the background worker will pick it up
	}()

	c.window.Close()
	// Refresh thread list if this was a reply.
	if req.ThreadID != "" {
		c.app.mailList.LoadFirstPage()
	}
}

func (c *ComposeWindow) saveDraft(identityByOption map[string]*mailpkg.Identity) {
	selected := c.identitySelect.Selected
	identity, ok := identityByOption[selected]
	if !ok || identity == nil {
		return
	}

	var inReplyTo string
	var refs []string
	var threadID string
	if c.replyTo != nil {
		inReplyTo = c.replyTo.MessageID
		refs = c.replyTo.BuildReplyReferences()
		threadID = c.replyTo.ThreadID
	}

	draft := &mailpkg.Draft{
		IdentityID:   identity.ID,
		ThreadID:     threadID,
		ToAddresses:  splitAddresses(c.toEntry.Text),
		CcAddresses:  splitAddresses(c.ccEntry.Text),
		Subject:      c.subjectEntry.Text,
		BodyText:     c.bodyEntry.Text,
		InReplyTo:    inReplyTo,
		References:   refs,
		IsReply:      c.replyTo != nil,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	go func() {
		if err := c.app.store.SaveDraft(context.Background(), draft); err != nil {
			log.Printf("compose: save draft failed: %v", err)
		}
	}()
}

func splitAddresses(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

var errNoIdentity = errors.New("please select a sending identity")
