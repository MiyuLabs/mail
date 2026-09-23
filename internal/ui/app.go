// Package ui — app.go is the top-level application window and layout.
// Three-pane layout: Sidebar | ThreadList | ThreadView.
// All state mutations flow through AppState, which notifies the UI via channels.
package ui

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/google/uuid"

	"github.com/MiyuLabs/mail/internal/imap"
	mailpkg "github.com/MiyuLabs/mail/internal/mail"
	"github.com/MiyuLabs/mail/internal/resend"
	"github.com/MiyuLabs/mail/internal/store"
)

// AppState is the central mutable state of the application.
// UI components read from and write to this struct (with appropriate locking).
type AppState struct {
	Identities       []mailpkg.Identity
	ActiveIdentityID string // "" = "All Inboxes" view
	Threads          []mailpkg.Thread
	SelectedThreadID string
	Drafts           []mailpkg.Draft
	SyncStatus       SyncStatus
	ResendQuota      resend.QuotaInfo
}

// SyncStatus represents the current sync state for the status bar.
type SyncStatus struct {
	Connected   bool
	Syncing     bool
	LastSynced  string // human-readable timestamp
	ErrorMsg    string
}

// App is the top-level application container.
type App struct {
	fyneApp  fyne.App
	window   fyne.Window
	state    *AppState

	// Sub-components
	sidebar    *Sidebar
	mailList   *MailList
	threadView *ThreadView
	rightPane  *fyne.Container // Container that swaps between mailList and threadView

	store      *store.Store
	syncEngine *imap.SyncEngine
	sender     *resend.Sender
	ctx        context.Context
	cancel     context.CancelFunc

	newMail    chan *mailpkg.Message
}

// NewApp creates and wires up the application.
func NewApp(
	fyneApp fyne.App,
	store *store.Store,
	syncEngine *imap.SyncEngine,
	sender *resend.Sender,
) *App {
	ctx, cancel := context.WithCancel(context.Background())

	a := &App{
		fyneApp:    fyneApp,
		ctx:        ctx,
		cancel:     cancel,
		store:      store,
		syncEngine: syncEngine,
		sender:     sender,
		state: &AppState{
			SyncStatus: SyncStatus{},
		},
		newMail: make(chan *mailpkg.Message, 32),
	}
	return a
}

// Run initialises the UI and starts the event loop. Blocks until the window closes.
func (a *App) Run() {
	a.window = a.fyneApp.NewWindow("mail")
	a.window.SetMaster()
	a.window.Resize(fyne.NewSize(1280, 800))
	a.window.SetTitle("MiyuMail")

	// ── Sub-component initialisation ─────────────────────────────────────────
	a.sidebar = NewSidebar(a)
	a.mailList = NewMailList(a)
	a.threadView = NewThreadView(a)

	// ── Layout: HSplit(sidebar, rightPane) ────────────────────────────────────
	// Sidebar is fixed-width; right pane swaps between MailList and ThreadView
	a.rightPane = container.NewMax(a.mailList.Container())

	mainSplit := container.NewHSplit(
		a.sidebar.Container(),
		a.rightPane,
	)
	mainSplit.SetOffset(0.20) // sidebar gets 20% of total width

	// ── Status bar at the bottom ───────────────────────────────────────────────
	statusBar := NewStatusBar(a)

	root := container.NewBorder(
		nil,                // top
		statusBar.Widget(), // bottom
		nil,                // left
		nil,                // right
		mainSplit,          // center (fills remaining space)
	)

	a.window.SetContent(root)

	// ── Keyboard shortcuts ────────────────────────────────────────────────────
	a.setupKeyboardShortcuts()

	// ── Background goroutines ─────────────────────────────────────────────────
	go a.loadInitialData()

	a.window.SetOnClosed(func() {
		a.cancel()
	})
	
	a.window.Show()
}

// loadInitialData fetches identities and the first page of threads from D1.
func (a *App) loadInitialData() {
	ids, err := a.store.ListIdentities(a.ctx)
	if err != nil {
		log.Printf("app: failed to load identities: %v", err)
		return
	}
	a.state.Identities = ids

	// Refresh sidebar on main goroutine.
	go a.processNewMail()
	go a.processOutbox()

	fyne.Do(func() {
		a.sidebar.Refresh()
		a.mailList.LoadFirstPage()
	})
}

// processNewMail listens for newly synced messages and delivers OS notifications.
func (a *App) processNewMail() {
	for {
		select {
		case <-a.ctx.Done():
			return
		case msg := <-a.newMail:
			if msg == nil {
				continue
			}
			// Deliver OS notification.
			a.fyneApp.SendNotification(&fyne.Notification{
				Title:   "New mail — " + msg.OriginalTo,
				Content: msg.FromAddress + ": " + msg.Subject,
			})
			// Refresh thread list on main goroutine.
			fyne.Do(func() {
				a.mailList.Prepend(msg)
				a.sidebar.RefreshBadges()
			})
		}
	}
}

// processOutbox periodically checks the outbox queue and attempts to send pending messages.
func (a *App) processOutbox() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			// Ensure sender exists (might be nil in some dev setups)
			if a.sender == nil {
				continue
			}

			// Fetch pending outbox messages
			rows, err := a.store.Local().GetPendingOutbox(a.ctx)
			if err != nil {
				log.Printf("outbox: error fetching pending items: %v", err)
				continue
			}

			type pendingItem struct {
				id, messageID, identityID, payload string
				retryCount int
			}
			var pending []pendingItem

			for rows.Next() {
				var item pendingItem
				if err := rows.Scan(&item.id, &item.messageID, &item.identityID, &item.payload, &item.retryCount); err == nil {
					pending = append(pending, item)
				}
			}
			rows.Close()

			for _, item := range pending {
				id, messageID, identityID, payload, retryCount := item.id, item.messageID, item.identityID, item.payload, item.retryCount

				var req mailpkg.ComposeRequest
				if err := json.Unmarshal([]byte(payload), &req); err != nil {
					log.Printf("outbox worker: unmarshal failed for %s: %v", id, err)
					_ = a.store.Local().UpdateOutbox(a.ctx, id, "failed", err.Error(), time.Now().Format(time.RFC3339), retryCount)
					continue
				}

				log.Printf("outbox worker: attempting to send %s (retry %d)", id, retryCount)

				// Send
				_, resendID, err := a.sender.Send(a.ctx, &req)
				if err != nil {
					log.Printf("outbox: send failed for %s: %v", id, err)
					
					// Decide if it's retryable
					retryable := true
					isNetworkErr := strings.Contains(err.Error(), "dial tcp") || strings.Contains(err.Error(), "connection") || strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "no such host")

					if strings.Contains(err.Error(), "400") || strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403") {
						retryable = false
					}

					if retryable {
						if isNetworkErr {
							// Infinite retries for offline / network errors
							nextRetry := time.Now().Add(30 * time.Second)
							_ = a.store.Local().UpdateOutbox(a.ctx, id, "pending", err.Error(), nextRetry.Format(time.RFC3339), retryCount)
						} else if retryCount < 3 {
							// 5xx errors, up to 3 retries
							nextRetry := time.Now().Add(time.Duration(retryCount+1) * 5 * time.Minute)
							_ = a.store.Local().UpdateOutbox(a.ctx, id, "pending", err.Error(), nextRetry.Format(time.RFC3339), retryCount+1)
						} else {
							_ = a.store.Local().UpdateOutbox(a.ctx, id, "failed", err.Error(), time.Now().Format(time.RFC3339), retryCount)
							a.fyneApp.SendNotification(&fyne.Notification{
								Title:   "Failed to send email",
								Content: "To " + strings.Join(req.To, ", "),
							})
						}
					} else {
						_ = a.store.Local().UpdateOutbox(a.ctx, id, "failed", err.Error(), time.Now().Format(time.RFC3339), retryCount)
						a.fyneApp.SendNotification(&fyne.Notification{
							Title:   "Failed to send email",
							Content: "To " + strings.Join(req.To, ", "),
						})
					}
					continue
				}

				// Success
				log.Printf("outbox worker: sent %s successfully (resend_id: %s)", id, resendID)
				
				// Mark as sent in outbox
				_ = a.store.Local().UpdateOutbox(a.ctx, id, "sent", "", time.Now().Format(time.RFC3339), retryCount)
				
				if req.ThreadID == "" {
					newThread := &mailpkg.Thread{
						ID:            uuid.New().String(),
						Subject:       mailpkg.NormalizeSubject(req.Subject),
						IdentityID:    identityID,
						LastMessageAt: time.Now(),
						MessageCount:  1,
						Snippet:       mailpkg.MakeSnippet(req.BodyText, 120),
						CreatedAt:     time.Now(),
						UpdatedAt:     time.Now(),
					}
					_ = a.store.Local().UpsertThread(a.ctx, newThread)
					_ = a.store.Remote().UpsertThread(a.ctx, newThread)
					req.ThreadID = newThread.ID
				}

				sentMsg := req.ToMessage(messageID, resendID)
				if err := a.store.RecordSent(a.ctx, sentMsg); err != nil {
					log.Printf("outbox: D1 record sent failed: %v", err)
				} else {
					a.RefreshMailList()
					if a.state.SelectedThreadID == sentMsg.ThreadID {
						a.SelectThread(sentMsg.ThreadID)
					}
				}
			}
		}
	}
}

// ShowThreadView switches the right pane to the thread view.
func (a *App) ShowThreadView(threadID string) {
	a.threadView.LoadThread(threadID)
	a.rightPane.Objects = []fyne.CanvasObject{a.threadView.Container()}
	a.rightPane.Refresh()
}

// ShowMailList switches the right pane to the mail list.
func (a *App) ShowMailList() {
	a.rightPane.Objects = []fyne.CanvasObject{a.mailList.Container()}
	a.rightPane.Refresh()
	a.mailList.ClearSelection()
}

// SelectIdentity filters the thread list to a specific identity.
// identityID == "" means "All Inboxes".
func (a *App) SelectIdentity(identityID string) {
	a.state.ActiveIdentityID = identityID
	a.state.SelectedThreadID = ""
	fyne.Do(func() {
		a.mailList.LoadFirstPage()
		a.threadView.Clear()
	})
}

// RefreshMailList reloads the current thread list. Useful after background sync finishes.
func (a *App) RefreshMailList() {
	fyne.Do(func() {
		a.mailList.LoadFirstPage()
	})
}

// SelectThread opens a thread in the right pane.
func (a *App) SelectThread(threadID string) {
	a.state.SelectedThreadID = threadID
	fyne.Do(func() {
		a.threadView.LoadThread(threadID)
	})
}

// OpenCompose opens a new compose window.
func (a *App) OpenCompose(identityID string) {
	w := NewComposeWindow(a, nil, identityID)
	w.Show()
}

// OpenReply opens a reply compose window for the given message.
func (a *App) OpenReply(msg *mailpkg.Message) {
	w := NewComposeWindow(a, msg, "")
	w.Show()
}

// NotifyNewMessage is called by the sync engine when a new message arrives.
func (a *App) NotifyNewMessage(msg *mailpkg.Message) {
	select {
	case a.newMail <- msg:
	default:
	}
}

func (a *App) setupKeyboardShortcuts() {
	a.window.Canvas().SetOnTypedKey(func(key *fyne.KeyEvent) {
		switch key.Name {
		case "c":
			a.OpenCompose(a.state.ActiveIdentityID)
		case "r":
			// Reply to selected thread's last message — handled by ThreadView.
			if a.threadView != nil {
				a.threadView.ReplyToLatest()
			}
		case "e":
			// Archive selected thread.
			if a.state.SelectedThreadID != "" {
				a.mailList.ArchiveThread(a.state.SelectedThreadID)
			}
		case "j":
			a.mailList.SelectNext()
		case "k":
			a.mailList.SelectPrev()
		case fyne.KeyEscape:
			a.threadView.Clear()
		}
	})
}

// ── Stub implementations for sub-components used above ───────────────────────
// Full implementations are in their respective files.

func (s *StatusBar) Widget() fyne.CanvasObject {
	return s.bar
}

// StatusBar displays sync status and quota at the bottom of the window.
type StatusBar struct {
	app *App
	bar *widget.Label
}

func NewStatusBar(a *App) *StatusBar {
	sb := &StatusBar{app: a, bar: widget.NewLabel("⚡ Synced")}
	sb.bar.Alignment = fyne.TextAlignTrailing
	return sb
}

func (s *StatusBar) Update(status SyncStatus) {
	text := "⚡ Synced"
	if status.Syncing {
		text = "⟳ Syncing…"
	} else if !status.Connected {
		text = "⚠ Disconnected"
	} else if status.ErrorMsg != "" {
		text = "✗ " + status.ErrorMsg
	}
	fyne.Do(func() { s.bar.SetText(text) })
}
