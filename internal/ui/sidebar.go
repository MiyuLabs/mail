// Package ui — sidebar.go renders the identity/mailbox navigation panel.
// Shows each @domain.tld identity with its accent color dot and unread count.
// Send-only identities (noreply@) show (—) and have no inbox.
package ui

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	mailpkg "github.com/MiyuLabs/mail/internal/mail"
)

// Sidebar is the left navigation panel showing identity mailboxes.
type Sidebar struct {
	app       *App
	container fyne.CanvasObject
	list      *widget.List
	// unread counts by identity ID
	unreadCounts map[string]int
}

// NewSidebar creates the sidebar component.
func NewSidebar(a *App) *Sidebar {
	s := &Sidebar{
		app:          a,
		unreadCounts: make(map[string]int),
	}

	// ── "All Inboxes" button at the top ───────────────────────────────────────
	allInboxBtn := widget.NewButton("  All Inboxes", func() {
		a.SelectIdentity("")
	})
	allInboxBtn.Alignment = widget.ButtonAlignLeading

	// ── Identity list ─────────────────────────────────────────────────────────
	s.list = widget.NewList(
		func() int {
			return len(a.state.Identities)
		},
		func() fyne.CanvasObject {
			return newIdentityRow()
		},
		func(id widget.ListItemID, item fyne.CanvasObject) {
			if id >= len(a.state.Identities) {
				return
			}
			identity := a.state.Identities[id]
			unread := s.unreadCounts[identity.ID]
			updateIdentityRow(item, identity, id, unread)
		},
	)
	s.list.OnSelected = func(id widget.ListItemID) {
		if id < len(a.state.Identities) {
			a.SelectIdentity(a.state.Identities[id].ID)
		}
	}

	// ── Bottom actions ────────────────────────────────────────────────────────
	composeBtn := widget.NewButtonWithIcon("  New Message", theme.DocumentCreateIcon(), func() {
		a.OpenCompose(a.state.ActiveIdentityID)
	})
	composeBtn.Importance = widget.HighImportance

	draftsBtn := widget.NewButton("  Drafts", func() {
		// TODO: open drafts panel
	})
	draftsBtn.Alignment = widget.ButtonAlignLeading

	// ── App title ─────────────────────────────────────────────────────────────
	title := widget.NewLabelWithStyle("MiyuMail", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	titleContainer := container.NewPadded(title)

	bottomSection := container.NewVBox(
		widget.NewSeparator(),
		draftsBtn,
		container.NewPadded(composeBtn),
	)

	s.container = container.NewBorder(
		container.NewVBox(titleContainer, widget.NewSeparator(), allInboxBtn), // top
		bottomSection, // bottom
		nil, nil,
		s.list, // center
	)

	return s
}

// Container returns the sidebar's root CanvasObject.
func (s *Sidebar) Container() fyne.CanvasObject {
	return s.container
}

// Refresh re-renders the identity list after data changes.
func (s *Sidebar) Refresh() {
	s.list.Refresh()
}

// RefreshBadges updates unread count badges.
func (s *Sidebar) RefreshBadges() {
	// TODO: fetch unread counts from state and call s.list.Refresh()
	s.list.Refresh()
}

// ──────────────────────────────────────────────────────────────────────────────
// Identity row helpers
// ──────────────────────────────────────────────────────────────────────────────

// newIdentityRow creates a template identity row widget.
func newIdentityRow() fyne.CanvasObject {
	// Avatar circle (initials).
	dot := canvas.NewCircle(color.White)
	// Wrap in a fixed-size container since canvas.Circle has no MinSize method.
	dotBox := container.NewWithoutLayout(dot)
	dotBox.Resize(fyne.NewSize(6, 6))
	dot.Resize(fyne.NewSize(6, 6))

	// Vertically center the dot
	paddedDot := container.NewPadded(container.NewCenter(dotBox))

	name := widget.NewLabel("identity@domain.in")
	name.TextStyle = fyne.TextStyle{Bold: false}

	badge := widget.NewLabel("")
	badge.Alignment = fyne.TextAlignTrailing
	badge.TextStyle = fyne.TextStyle{Bold: true}

	return container.NewHBox(paddedDot, name, layout.NewSpacer(), badge)
}

// updateIdentityRow populates an identity row with live data.
func updateIdentityRow(item fyne.CanvasObject, id mailpkg.Identity, idx int, unread int) {
	box := item.(*fyne.Container)
	if len(box.Objects) < 4 {
		return
	}

	// Colored dot
	paddedDot, ok := box.Objects[0].(*fyne.Container)
	if ok && len(paddedDot.Objects) > 0 {
		centerDot, ok := paddedDot.Objects[0].(*fyne.Container)
		if ok && len(centerDot.Objects) > 0 {
			dotBox, ok := centerDot.Objects[0].(*fyne.Container)
			if ok && len(dotBox.Objects) > 0 {
				if dot, ok := dotBox.Objects[0].(*canvas.Circle); ok {
			if id.IsSendOnly() {
				dot.FillColor = SendOnlyColor
			} else {
				dot.FillColor = IdentityAccentColorFor(idx)
			}
					dot.Refresh()
				}
			}
		}
	}

	// Address label.
	nameLabel := box.Objects[1].(*widget.Label)
	nameLabel.SetText(id.Address)
	if id.IsSendOnly() {
		nameLabel.TextStyle.Italic = true
		// Can't easily change text color dynamically on widget.Label in Fyne without a custom widget,
		// but we can rely on italic to signify it's muted, which we already do.
	} else {
		nameLabel.TextStyle.Italic = false
	}

	// Unread badge.
	badge := box.Objects[3].(*widget.Label)
	if id.IsSendOnly() {
		badge.SetText("(—)")
	} else if unread > 0 {
		badge.SetText(fmt.Sprintf("%d", unread))
	} else {
		badge.SetText("")
	}

	box.Refresh()
}

