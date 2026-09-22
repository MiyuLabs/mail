// Package ui — theme.go defines the custom dark theme for the mail client.
// Design philosophy: deep charcoal, per-identity accent colors, monospace
// addresses, minimal chrome. Not a Material Design clone.
package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// ──────────────────────────────────────────────────────────────────────────────
// Color palette
// ──────────────────────────────────────────────────────────────────────────────

var (
	// Backgrounds
	colorBG         = color.NRGBA{R: 16, G: 16, B: 20, A: 255}   // #101014 — deepest background
	colorBGSurface  = color.NRGBA{R: 23, G: 23, B: 29, A: 255}   // #17171D — surface
	colorBGCard     = color.NRGBA{R: 23, G: 23, B: 29, A: 255}   // Same as surface for cleaner look
	colorBGHover    = color.NRGBA{R: 29, G: 29, B: 37, A: 255}   // #1D1D25 — hover state
	colorBGSelected = color.NRGBA{R: 37, G: 36, B: 50, A: 255}   // #252432 — selected

	// Text
	colorTextPrimary   = color.NRGBA{R: 238, G: 238, B: 242, A: 255} // #EEEEF2
	colorTextSecondary = color.NRGBA{R: 180, G: 180, B: 190, A: 255} // #B4B4BE
	colorTextMuted     = color.NRGBA{R: 127, G: 127, B: 139, A: 255} // #7F7F8B

	// Borders / separators
	colorBorder    = color.NRGBA{R: 37, G: 37, B: 45, A: 255}  // #25252D
	colorSeparator = color.NRGBA{R: 37, G: 37, B: 45, A: 255}  // #25252D

	// Primary accent — #806CFF
	colorAccent      = color.NRGBA{R: 128, G: 108, B: 255, A: 255} // #806CFF
	colorAccentMuted = color.NRGBA{R: 128, G: 108, B: 255, A: 40}  // translucent

	// Status
	colorError   = color.NRGBA{R: 255, G: 82, B: 82, A: 255}   // #FF5252
	colorWarning = color.NRGBA{R: 255, G: 183, B: 77, A: 255}  // #FFB74D
	colorSuccess = color.NRGBA{R: 79, G: 209, B: 131, A: 255}  // #4FD183
)

// IdentityAccentColors maps identity position (0-based index) to accent color.
// These are applied as colored dots in the sidebar.
var IdentityAccentColors = []color.Color{
	color.NRGBA{R: 45, G: 212, B: 191, A: 255},  // Teal   — careers@
	color.NRGBA{R: 245, G: 158, B: 11, A: 255},  // Amber  — legal@
	color.NRGBA{R: 139, G: 92, B: 246, A: 255},  // Violet — hi@
	color.NRGBA{R: 249, G: 115, B: 22, A: 255},  // Orange
	color.NRGBA{R: 236, G: 72, B: 153, A: 255},  // Pink
	color.NRGBA{R: 34, G: 211, B: 238, A: 255},  // Cyan
	color.NRGBA{R: 163, G: 230, B: 53, A: 255},  // Lime
}

// IdentityAccentColorFor returns the accent color for an identity at a given index.
func IdentityAccentColorFor(idx int) color.Color {
	return IdentityAccentColors[idx%len(IdentityAccentColors)]
}

// SendOnlyColor is the muted color for send-only identities (e.g. noreply@).
var SendOnlyColor = color.NRGBA{R: 100, G: 100, B: 120, A: 255}

// ──────────────────────────────────────────────────────────────────────────────
// MailTheme — Fyne Theme implementation
// ──────────────────────────────────────────────────────────────────────────────

// MailTheme is the custom Fyne theme for the mail client.
type MailTheme struct{}

var _ fyne.Theme = (*MailTheme)(nil)

// Color returns the themed color for a given color name and variant.
func (t *MailTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return colorBG
	case theme.ColorNameMenuBackground:
		return colorBGSurface
	case theme.ColorNameOverlayBackground:
		return colorBGCard
	case theme.ColorNameButton:
		return colorBGCard
	case theme.ColorNameDisabledButton:
		return colorBGSurface
	case theme.ColorNameHover:
		return colorBGHover
	case theme.ColorNameFocus:
		return colorAccentMuted
	case theme.ColorNameSelection:
		return colorBGSelected
	case theme.ColorNameForeground:
		return colorTextPrimary
	case theme.ColorNameDisabled:
		return colorTextMuted
	case theme.ColorNamePlaceHolder:
		return colorTextSecondary
	case theme.ColorNamePrimary:
		return colorAccent
	case theme.ColorNameError:
		return colorError
	case theme.ColorNameSeparator:
		return colorSeparator
	case theme.ColorNameInputBackground:
		return colorBGCard
	case theme.ColorNameInputBorder:
		return colorBorder
	case theme.ColorNameScrollBar:
		return colorTextMuted
	case theme.ColorNameShadow:
		return color.NRGBA{A: 80}
	}
	return theme.DefaultTheme().Color(name, theme.VariantDark)
}

// Font delegates to the default theme (Inter-like system font).
func (t *MailTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

// Icon delegates to the default theme icons.
func (t *MailTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

// Size returns custom size values for a tighter, more desktop-friendly layout.
func (t *MailTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return 12 // Increased padding
	case theme.SizeNameInnerPadding:
		return 8
	case theme.SizeNameText:
		return 14 // Increased base font size
	case theme.SizeNameHeadingText:
		return 20
	case theme.SizeNameSubHeadingText:
		return 16
	case theme.SizeNameCaptionText:
		return 12
	case theme.SizeNameInputBorder:
		return 1
	case theme.SizeNameScrollBar:
		return 6
	case theme.SizeNameScrollBarSmall:
		return 3
	}
	return theme.DefaultTheme().Size(name)
}
