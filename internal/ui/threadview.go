// Package ui — threadview.go renders an open email thread as a conversation.
// Messages appear as stacked cards in chronological order.
// The reply bar at the bottom automatically uses the correct From identity.
package ui

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	mailpkg "github.com/MiyuLabs/mail/internal/mail"
	"github.com/MiyuLabs/mail/internal/util"
)

// ThreadView renders the open thread in the right pane.
type ThreadView struct {
	app       *App
	root      fyne.CanvasObject
	scroll    *container.Scroll
	msgStack  *fyne.Container
	replyBar  *replyBarWidget
	thread    *mailpkg.Thread
	emptyLbl  *widget.Label
}

// NewThreadView creates the thread view panel.
func NewThreadView(a *App) *ThreadView {
	tv := &ThreadView{app: a}

	tv.emptyLbl = widget.NewLabel("Select a thread to read")
	tv.emptyLbl.Alignment = fyne.TextAlignCenter

	tv.msgStack = container.NewVBox()
	tv.scroll = container.NewScroll(tv.msgStack)

	tv.replyBar = newReplyBar(a, tv)

	tv.root = container.NewBorder(
		nil,           // top
		tv.replyBar.Container(), // bottom
		nil, nil,
		tv.scroll,     // center
	)

	// Show empty state initially.
	tv.Clear()
	return tv
}

// Container returns the thread view's root canvas object.
func (tv *ThreadView) Container() fyne.CanvasObject {
	return tv.root
}

// LoadThread fetches and renders a thread by ID.
func (tv *ThreadView) LoadThread(threadID string) {
	go func() {
		thread, err := tv.app.store.GetThread(context.Background(), threadID)
		if err != nil {
			log.Printf("threadview: load thread %s failed: %v", threadID, err)
			return
		}
		fyne.Do(func() {
			tv.renderThread(thread)
		})
	}()
}

// Clear resets the view to the empty state.
func (tv *ThreadView) Clear() {
	tv.thread = nil
	tv.msgStack.Objects = []fyne.CanvasObject{
		container.NewCenter(tv.emptyLbl),
	}
	tv.msgStack.Refresh()
	tv.replyBar.SetVisible(false)
}

// ReplyToLatest opens a reply compose for the thread's most recent message.
func (tv *ThreadView) ReplyToLatest() {
	if tv.thread == nil || len(tv.thread.Messages) == 0 {
		return
	}
	last := &tv.thread.Messages[len(tv.thread.Messages)-1]
	tv.app.OpenReply(last)
}

// renderThread builds the message card stack.
func (tv *ThreadView) renderThread(thread *mailpkg.Thread) {
	tv.thread = thread
	tv.msgStack.Objects = nil

	// Thread header with Back button.
	backBtn := widget.NewButtonWithIcon(" Back", theme.NavigateBackIcon(), func() {
		tv.app.ShowMailList()
	})
	backBtn.Importance = widget.LowImportance

	title := widget.NewLabelWithStyle(thread.Subject, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	// We want title to be larger, Fyne labels can use RichText for headings, but a bold label with heading scale works if we use a custom theme.
	// For now, wrapping it in a VBox with some padding looks clean.

	headerBox := container.NewBorder(nil, nil, backBtn, nil, container.NewPadded(title))
	tv.msgStack.Add(container.NewVBox(container.NewPadded(headerBox), widget.NewSeparator()))

	// Message cards.
	for i := range thread.Messages {
		msg := &thread.Messages[i]
		card := tv.buildMessageCard(msg)
		tv.msgStack.Add(card)
	}

	// Show reply bar for the last message's identity (if not send-only).
	if len(thread.Messages) > 0 {
		last := &thread.Messages[len(thread.Messages)-1]
		identity := tv.app.mailList.identityFor(last.OriginalTo)
		if identity == nil || !identity.IsSendOnly() {
			tv.replyBar.SetMessage(last)
			tv.replyBar.SetVisible(true)
		} else {
			tv.replyBar.SetVisible(false)
		}
	}

	tv.msgStack.Refresh()
	// Scroll to bottom to show latest message.
	tv.scroll.ScrollToBottom()
}

func (tv *ThreadView) buildMessageCard(msg *mailpkg.Message) fyne.CanvasObject {
	// ── Header ────────────────────────────────────────────────────────────────
	fromLabel := widget.NewLabel(msg.FromName + " <" + msg.FromAddress + ">")
	fromLabel.TextStyle = fyne.TextStyle{Bold: true}

	toLabel := widget.NewLabel("To: " + strings.Join(msg.ToAddresses, ", "))
	toLabel.TextStyle = fyne.TextStyle{Italic: true} // Secondary/Muted

	dateLabel := widget.NewLabel(msg.ReceivedAt.Format("Mon, 2 Jan 2006 15:04:05 MST"))
	dateLabel.Alignment = fyne.TextAlignTrailing
	dateLabel.TextStyle = fyne.TextStyle{Italic: true} // Muted

	headerRow := container.NewBorder(nil, nil, fromLabel, dateLabel)
	headerBox := container.NewVBox(headerRow, toLabel)

	// ── Body ─────────────────────────────────────────────────────────────────
	var bodyWidget fyne.CanvasObject

	if msg.BodyHTML != "" {
		segs := util.HTMLToRichText(msg.BodyHTML)
		richText := widget.NewRichText(segs...)
		richText.Wrapping = fyne.TextWrapWord

		viewOriginalBtn := widget.NewButtonWithIcon("View original", theme.BrokenImageIcon(), func() {
			if err := util.OpenHTMLInBrowser(msg.BodyHTML); err != nil {
				log.Printf("threadview: open in browser failed: %v", err)
			}
		})
		viewOriginalBtn.Importance = widget.LowImportance

		bodyWidget = container.NewVBox(richText, viewOriginalBtn)
	} else {
		textWidget := widget.NewLabel(msg.BodyText)
		textWidget.Wrapping = fyne.TextWrapWord
		bodyWidget = textWidget
	}

	// ── Attachments ───────────────────────────────────────────────────────────
	var attachRow fyne.CanvasObject
	if msg.HasAttachments && len(msg.Attachments) > 0 {
		attBox := container.NewHBox()
		for _, att := range msg.Attachments {
			attCopy := att
			btn := widget.NewButtonWithIcon(
				fmt.Sprintf("📎 %s (%s)", attCopy.Filename, humanizeBytes(attCopy.SizeBytes)),
				theme.DownloadIcon(),
				func() {
					if attCopy.StorageKey == "" {
						return
					}
					
					d := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
						if err != nil || uc == nil {
							return
						}
						defer uc.Close()
						
						src, err := os.Open(attCopy.StorageKey)
						if err != nil {
							dialog.ShowError(err, tv.app.window)
							return
						}
						defer src.Close()
						
						if _, err := io.Copy(uc, src); err != nil {
							dialog.ShowError(fmt.Errorf("failed to save attachment: %w", err), tv.app.window)
						}
					}, tv.app.window)
					d.SetFileName(attCopy.Filename)
					d.Show()
				},
			)
			btn.Importance = widget.LowImportance
			attBox.Add(btn)
		}
		attachRow = container.NewVBox(widget.NewSeparator(), attBox)
	}

	// ── Reply action ──────────────────────────────────────────────────────────
	replyBtn := widget.NewButtonWithIcon("Reply", theme.MailReplyIcon(), func() {
		tv.app.OpenReply(msg)
	})
	replyBtn.Importance = widget.LowImportance

	forwardBtn := widget.NewButtonWithIcon("Forward", theme.MailForwardIcon(), func() {
		// TODO: build forward compose window
	})
	forwardBtn.Importance = widget.LowImportance

	actionRow := container.NewHBox(replyBtn, forwardBtn)

	var parts []fyne.CanvasObject
	parts = append(parts, headerBox, widget.NewSeparator(), container.NewPadded(bodyWidget))
	if attachRow != nil {
		parts = append(parts, attachRow)
	}
	parts = append(parts, actionRow)

	cardContent := container.NewPadded(container.NewVBox(parts...))
	return container.NewVBox(cardContent, widget.NewSeparator())
}

// ──────────────────────────────────────────────────────────────────────────────
// Reply bar
// ──────────────────────────────────────────────────────────────────────────────

type replyBarWidget struct {
	app       *App
	tv        *ThreadView
	container fyne.CanvasObject
	entry     *widget.Entry
	fromLabel *widget.Label
	sendBtn   *widget.Button
	msg       *mailpkg.Message
	visible   bool
}

func newReplyBar(a *App, tv *ThreadView) *replyBarWidget {
	rb := &replyBarWidget{app: a, tv: tv}

	rb.fromLabel = widget.NewLabel("Reply as …")
	rb.fromLabel.TextStyle = fyne.TextStyle{Italic: true}

	rb.entry = widget.NewMultiLineEntry()
	rb.entry.SetPlaceHolder("Type your reply…")
	rb.entry.SetMinRowsVisible(3)

	rb.sendBtn = widget.NewButtonWithIcon("Send", theme.MailSendIcon(), func() {
		rb.send()
	})
	rb.sendBtn.Importance = widget.HighImportance

	rb.container = container.NewBorder(
		container.NewVBox(widget.NewSeparator(), rb.fromLabel),
		nil,
		nil,
		rb.sendBtn,
		rb.entry,
	)

	return rb
}

func (rb *replyBarWidget) Container() fyne.CanvasObject {
	return rb.container
}

func (rb *replyBarWidget) SetMessage(msg *mailpkg.Message) {
	rb.msg = msg
	identity := rb.app.mailList.identityFor(msg.OriginalTo)
	if identity != nil {
		rb.fromLabel.SetText("↩ Reply as " + identity.Address)
	} else {
		rb.fromLabel.SetText("↩ Reply")
	}
	rb.entry.SetText("")
}

func (rb *replyBarWidget) SetVisible(v bool) {
	rb.visible = v
	if v {
		rb.container.Show()
	} else {
		rb.container.Hide()
	}
}

func (rb *replyBarWidget) send() {
	if rb.msg == nil || strings.TrimSpace(rb.entry.Text) == "" {
		return
	}
	rb.app.OpenReply(rb.msg)
}

// ──────────────────────────────────────────────────────────────────────────────
// Utilities
// ──────────────────────────────────────────────────────────────────────────────

func humanizeBytes(n int64) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.0f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
