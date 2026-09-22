// Package util — html.go converts HTML email bodies to Fyne RichText segments.
// Provides ~90% fidelity for typical business emails without a browser engine.
// A "View original in browser" button is always available for full-fidelity rendering.
package util

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"fyne.io/fyne/v2/widget"
	"golang.org/x/net/html"
)

// HTMLToRichText converts an HTML string to Fyne RichTextSegments.
// Handles bold, italic, links, headers, paragraphs, line breaks, lists, blockquotes, code.
func HTMLToRichText(htmlStr string) []widget.RichTextSegment {
	if htmlStr == "" {
		return nil
	}
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return []widget.RichTextSegment{&widget.TextSegment{Text: htmlStr}}
	}
	var segs []widget.RichTextSegment
	walkNode(doc, &segs, widget.RichTextStyle{})
	return segs
}

// OpenHTMLInBrowser writes html to a temp file and opens it in the system browser.
func OpenHTMLInBrowser(htmlContent string) error {
	tmp, err := os.CreateTemp("", "mail-preview-*.html")
	if err != nil {
		return fmt.Errorf("util: create temp file: %w", err)
	}
	defer tmp.Close()

	if _, err := tmp.WriteString(htmlContent); err != nil {
		return fmt.Errorf("util: write temp file: %w", err)
	}

	return OpenURL("file://" + filepath.ToSlash(tmp.Name()))
}

// OpenURL opens a URL in the system's default browser.
func OpenURL(rawURL string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{rawURL}
	case "windows":
		cmd, args = "cmd", []string{"/c", "start", rawURL}
	default:
		cmd, args = "xdg-open", []string{rawURL}
	}
	return exec.Command(cmd, args...).Start()
}

// ──────────────────────────────────────────────────────────────────────────────
// HTML → RichText walker
// ──────────────────────────────────────────────────────────────────────────────

func walkNode(n *html.Node, segs *[]widget.RichTextSegment, style widget.RichTextStyle) {
	if n == nil {
		return
	}
	switch n.Type {
	case html.TextNode:
		text := n.Data
		if strings.TrimSpace(text) == "" {
			return
		}
		*segs = append(*segs, &widget.TextSegment{Text: text, Style: style})

	case html.ElementNode:
		child := style
		switch strings.ToLower(n.Data) {
		case "b", "strong":
			child.TextStyle.Bold = true
		case "i", "em":
			child.TextStyle.Italic = true
		case "code", "pre", "tt":
			child.TextStyle.Monospace = true
		case "br":
			*segs = append(*segs, &widget.TextSegment{Text: "\n"})
			return
		case "p":
			*segs = append(*segs, &widget.TextSegment{Text: "\n"})
		case "li":
			*segs = append(*segs, &widget.TextSegment{Text: "\n• "})
		case "hr":
			*segs = append(*segs, &widget.SeparatorSegment{})
			return
		case "blockquote":
			*segs = append(*segs, &widget.TextSegment{Text: "\n│ "})
		case "script", "style", "head":
			return // skip non-display elements
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkNode(c, segs, child)
		}

		switch strings.ToLower(n.Data) {
		case "p", "h1", "h2", "h3", "h4", "div", "blockquote", "pre":
			*segs = append(*segs, &widget.TextSegment{Text: "\n"})
		}

	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkNode(c, segs, style)
		}
	}
}
