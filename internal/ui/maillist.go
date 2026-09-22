// Package ui — maillist.go renders the paginated thread list.
// Loads 30 threads at a time, appends more on scroll-to-bottom.
// Quick actions (archive, star) appear on hover.
package ui

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	mailpkg "github.com/MiyuLabs/mail/internal/mail"
)

// MailList renders the thread list with on-demand pagination.
type MailList struct {
	app         *App
	root        fyne.CanvasObject
	list        *widget.List
	threads     []mailpkg.Thread
	nextCursor  string
	hasMore     bool
	loading     bool
	searchEntry *widget.Entry
	selectedIdx int
	fetchGen    int64
	searchQuery string
}

// NewMailList creates the thread list panel.
func NewMailList(a *App) *MailList {
	ml := &MailList{
		app:         a,
		selectedIdx: -1,
	}

	// ── Search bar ────────────────────────────────────────────────────────────
	ml.searchEntry = widget.NewEntry()
	ml.searchEntry.SetPlaceHolder("Search threads…")
	// Wrap in padded container to avoid harsh default widget appearance
	searchBox := container.NewPadded(ml.searchEntry)

	ml.searchEntry.OnChanged = func(q string) {
		ml.searchQuery = strings.TrimSpace(q)
		ml.LoadFirstPage()
	}

	// ── Thread list ───────────────────────────────────────────────────────────
	ml.list = widget.NewList(
		func() int {
			count := len(ml.threads)
			if ml.hasMore {
				count++ // extra row for "Load more…" sentinel
			}
			return count
		},
		func() fyne.CanvasObject {
			return newThreadRow()
		},
		func(id widget.ListItemID, item fyne.CanvasObject) {
			// Sentinel "load more" row.
			if ml.hasMore && id == len(ml.threads) {
				if box, ok := item.(*fyne.Container); ok {
					box.Objects = []fyne.CanvasObject{
						widget.NewLabel("Load more…"),
					}
					box.Refresh()
				}
				return
			}
			if id >= len(ml.threads) {
				return
			}
			t := &ml.threads[id]
			identity := ml.identityFor(t.IdentityID)
			updateThreadRow(ml, item, t, identity, id == ml.selectedIdx)
		},
	)

	ml.list.OnSelected = func(id widget.ListItemID) {
		if ml.hasMore && id == len(ml.threads) {
			ml.LoadNextPage()
			return
		}
		if id < 0 || id >= len(ml.threads) {
			return
		}
		ml.selectedIdx = id
		t := ml.threads[id]
		
		// Unread -> Read instantly locally and remotely
		if !t.IsRead {
			ml.threads[id].IsRead = true
			ml.list.RefreshItem(id)
			_ = a.store.MarkThreadRead(a.ctx, t.ID)
		}

		a.state.SelectedThreadID = t.ID
		a.ShowThreadView(t.ID)
	}

	ml.list.OnUnselected = func(id widget.ListItemID) {
		if ml.selectedIdx == id {
			ml.selectedIdx = -1
		}
	}

	ml.root = container.NewBorder(
		container.NewVBox(searchBox, widget.NewSeparator()), // top
		nil,                 // bottom
		nil, nil,
		ml.list, // center
	)

	return ml
}

// Container returns the mail list's root canvas object.
func (ml *MailList) Container() fyne.CanvasObject {
	return ml.root
}

// LoadFirstPage fetches the first page of threads from D1.
func (ml *MailList) LoadFirstPage() {
	ml.threads = nil
	ml.nextCursor = ""
	ml.hasMore = false
	ml.loading = false
	ml.selectedIdx = -1
	ml.list.UnselectAll()
	ml.fetchGen++
	ml.fetchPage("", ml.fetchGen, ml.app.state.ActiveIdentityID, ml.searchQuery)
}

// LoadNextPage fetches the next batch of threads (called on scroll-to-bottom).
func (ml *MailList) LoadNextPage() {
	if ml.loading || !ml.hasMore {
		return
	}
	ml.fetchPage(ml.nextCursor, ml.fetchGen, ml.app.state.ActiveIdentityID, ml.searchQuery)
}

func (ml *MailList) fetchPage(cursor string, gen int64, identityID string, query string) {
	ml.loading = true
	go func() {
		var page *mailpkg.ThreadPage
		var err error
		req := mailpkg.PageRequest{
			IdentityID: identityID,
			Cursor:     cursor,
			Limit:      mailpkg.DefaultPageSize,
		}
		if query != "" {
			page, err = ml.app.store.SearchThreads(context.Background(), req, query)
		} else {
			page, err = ml.app.store.ListThreads(context.Background(), req)
		}
		
		if err != nil {
			log.Printf("maillist: load threads failed: %v", err)
			fyne.Do(func() {
				if ml.fetchGen == gen {
					ml.loading = false
				}
			})
			return
		}
		fyne.Do(func() {
			if ml.fetchGen != gen {
				return // stale request
			}
			ml.threads = append(ml.threads, page.Threads...)
			ml.nextCursor = page.NextCursor
			ml.hasMore = page.NextCursor != ""
			ml.loading = false
			ml.list.Refresh()
		})
	}()
}

// Prepend adds a new message's thread to the top of the list.
func (ml *MailList) Prepend(msg *mailpkg.Message) {
	// Find or create thread entry.
	for i, t := range ml.threads {
		if t.ID == msg.ThreadID {
			// Move to top.
			updated := ml.threads[i]
			updated.Snippet = msg.Snippet
			updated.LastMessageAt = msg.ReceivedAt
			updated.IsRead = false
			ml.threads = append([]mailpkg.Thread{updated}, append(ml.threads[:i], ml.threads[i+1:]...)...)
			ml.list.Refresh()
			return
		}
	}
}

// ArchiveThread marks a thread as archived.
func (ml *MailList) ArchiveThread(threadID string) {
	for i := range ml.threads {
		if ml.threads[i].ID == threadID {
			ml.threads = append(ml.threads[:i], ml.threads[i+1:]...)
			if ml.selectedIdx == i {
				ml.selectedIdx = -1
				ml.app.SelectThread("")
			} else if ml.selectedIdx > i {
				ml.selectedIdx--
			}
			ml.list.Refresh()
			break
		}
	}
	go func() {
		if err := ml.app.store.MarkThreadArchived(context.Background(), threadID); err != nil {
			log.Printf("maillist: archive failed: %v", err)
		}
	}()
}

// TrashThread marks a thread as trashed.
func (ml *MailList) TrashThread(threadID string) {
	for i := range ml.threads {
		if ml.threads[i].ID == threadID {
			ml.threads = append(ml.threads[:i], ml.threads[i+1:]...)
			if ml.selectedIdx == i {
				ml.selectedIdx = -1
				ml.app.SelectThread("")
			} else if ml.selectedIdx > i {
				ml.selectedIdx--
			}
			ml.list.Refresh()
			break
		}
	}
	go func() {
		if err := ml.app.store.MarkThreadTrashed(context.Background(), threadID); err != nil {
			log.Printf("maillist: trash failed: %v", err)
		}
	}()
}

// ToggleStar toggles a thread's star status.
func (ml *MailList) ToggleStar(threadID string) {
	var starred bool
	for i := range ml.threads {
		if ml.threads[i].ID == threadID {
			ml.threads[i].IsStarred = !ml.threads[i].IsStarred
			starred = ml.threads[i].IsStarred
			ml.list.Refresh()
			break
		}
	}
	go func() {
		if err := ml.app.store.MarkThreadStarred(context.Background(), threadID, starred); err != nil {
			log.Printf("maillist: star failed: %v", err)
		}
	}()
}

// SelectNext moves selection down.
func (ml *MailList) SelectNext() {
	if ml.selectedIdx+1 < len(ml.threads) {
		ml.list.Select(ml.selectedIdx + 1)
	}
}

// ClearSelection removes the current selection.
func (ml *MailList) ClearSelection() {
	if ml.selectedIdx >= 0 {
		ml.list.Unselect(ml.selectedIdx)
	}
}

// SelectPrev moves selection up. one row.
func (ml *MailList) SelectPrev() {
	if ml.selectedIdx > 0 {
		ml.selectedIdx--
		ml.list.Select(ml.selectedIdx)
	}
}

// identityFor returns the Identity for the given ID, or nil.
func (ml *MailList) identityFor(id string) *mailpkg.Identity {
	for i := range ml.app.state.Identities {
		if ml.app.state.Identities[i].ID == id {
			return &ml.app.state.Identities[i]
		}
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Thread row widget
// ──────────────────────────────────────────────────────────────────────────────

// subjectSnippetLayout gracefully shares space between subject and snippet.
// It gives the subject its natural width up to 50% of the total space,
// and the snippet gets the rest.
type subjectSnippetLayout struct{}

func (s *subjectSnippetLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	if len(objects) != 2 {
		return
	}
	subject, ok1 := objects[0].(*widget.Label)
	snippet, ok2 := objects[1].(*widget.Label)
	if !ok1 || !ok2 {
		return
	}

	// Measure natural width of the subject text directly, bypassing the truncated label's MinSize
	natSize := fyne.MeasureText(subject.Text, theme.TextSize(), subject.TextStyle)

	// Add generous padding to account for the Label widget's internal insets
	// so that it doesn't prematurely trigger the ellipsis.
	subjWidth := natSize.Width + theme.Padding()*4

	// Give the subject all the space it needs, up to the full available width.
	// We no longer artificially cap it at 50% or 75% because the subject is more important.
	if subjWidth > size.Width {
		subjWidth = size.Width
	}

	gap := theme.Padding() * 2

	subject.Resize(fyne.NewSize(subjWidth, size.Height))
	subject.Move(fyne.NewPos(0, 0))

	snippetWidth := size.Width - subjWidth - gap
	if snippetWidth < 0 {
		snippetWidth = 0
	}
	snippet.Resize(fyne.NewSize(snippetWidth, size.Height))
	snippet.Move(fyne.NewPos(subjWidth+gap, 0))
}

func (s *subjectSnippetLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(50, 20) // Provide a small minimum size so it can shrink
}

type threadRow struct {
	widget.BaseWidget
	sender  *widget.Label
	date    *widget.Label
	subject *widget.Label
	snippet *widget.Label
	starBtn *widget.Button
	archBtn *widget.Button
	trshBtn *widget.Button
	content *fyne.Container
}

func (r *threadRow) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(r.content)
}

func newThreadRow() fyne.CanvasObject {
	r := &threadRow{
		sender:  widget.NewLabel("Sender Name"),
		date:    widget.NewLabel("Sep 18"),
		subject: widget.NewLabel("Subject line here"),
		snippet: widget.NewLabel("- Preview of the message body..."),
		starBtn: widget.NewButtonWithIcon("", theme.NewThemedResource(theme.ConfirmIcon()), func() {}),
		archBtn: widget.NewButtonWithIcon("", theme.NewThemedResource(theme.CancelIcon()), func() {}),
		trshBtn: widget.NewButtonWithIcon("", theme.NewThemedResource(theme.DeleteIcon()), func() {}),
	}

	r.sender.TextStyle = fyne.TextStyle{Bold: true}
	r.date.Alignment = fyne.TextAlignTrailing
	r.date.TextStyle = fyne.TextStyle{Italic: true} // Muted appearance
	
	r.subject.Truncation = fyne.TextTruncateEllipsis
	r.snippet.TextStyle = fyne.TextStyle{Italic: true} // Muted appearance
	r.snippet.Truncation = fyne.TextTruncateEllipsis

	r.archBtn.Importance = widget.LowImportance
	r.trshBtn.Importance = widget.LowImportance
	r.starBtn.Importance = widget.LowImportance

	// Combine subject and snippet into our custom layout
	subjectBox := container.New(&subjectSnippetLayout{}, r.subject, r.snippet)

	// Combine actions and date
	rightBox := container.NewHBox(r.starBtn, r.archBtn, r.trshBtn, r.date)

	// Main row layout: sender on left, actions+date on right, subject+snippet in center
	row := container.NewBorder(nil, nil, r.sender, rightBox, subjectBox)
	r.content = container.NewPadded(row)
	
	r.ExtendBaseWidget(r)
	return r
}

func updateThreadRow(ml *MailList, item fyne.CanvasObject, t *mailpkg.Thread, identity *mailpkg.Identity, selected bool) {
	r, ok := item.(*threadRow)
	if !ok {
		return
	}

	r.date.SetText(formatThreadDate(t.LastMessageAt))

	if t.ParticipantNames != "" {
		r.sender.SetText(t.ParticipantNames)
	} else {
		r.sender.SetText(t.Subject)
	}
	r.sender.TextStyle.Bold = !t.IsRead

	r.subject.SetText(t.Subject)
	r.subject.TextStyle.Bold = true
	
	r.snippet.SetText(t.Snippet)

	if t.IsStarred {
		r.starBtn.SetIcon(theme.NewThemedResource(theme.ConfirmIcon())) // TODO: find better star icon
	} else {
		r.starBtn.SetIcon(theme.NewThemedResource(theme.DocumentSaveIcon())) // TODO: find better hollow star icon
	}
	
	r.starBtn.OnTapped = func() {
		ml.ToggleStar(t.ID)
	}
	
	r.archBtn.OnTapped = func() {
		ml.ArchiveThread(t.ID)
	}

	r.trshBtn.OnTapped = func() {
		ml.TrashThread(t.ID)
	}

	r.Refresh()
}

func threadInitials(t *mailpkg.Thread) string {
	name := t.ParticipantNames
	if name == "" {
		name = t.Subject
	}
	words := strings.Fields(name)
	if len(words) == 0 {
		return "??"
	}
	first := strings.ToUpper(string([]rune(words[0])[0]))
	if len(words) > 1 {
		second := strings.ToUpper(string([]rune(words[1])[0]))
		return first + second
	}
	return first
}

func formatThreadDate(t time.Time) string {
	now := time.Now()
	switch {
	case sameDay(t, now):
		return t.Format("3:04 PM")
	case sameDay(t, now.AddDate(0, 0, -1)):
		return "Yesterday"
	case t.Year() == now.Year():
		return t.Format("Jan 2")
	default:
		return fmt.Sprintf("%d/%02d/%02d", t.Year(), t.Month(), t.Day())
	}
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
