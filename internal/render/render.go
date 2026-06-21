// Package render converts item bodies (HTML -> markdown -> Glamour) into
// width-aware terminal output with a plain-text fallback (DESIGN.md §7). The
// HTML is sanitized during the markdown conversion, so scripts and styles never
// reach the terminal.
package render

import (
	"fmt"
	"os"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"

	"github.com/srhoton/renomail/internal/model"
)

// Renderer turns an item body into terminal-ready styled text. It is width-aware
// (the reader rebuilds it on a terminal resize via SetWidth) and picks a light or
// dark Glamour theme from the detected terminal background. A Renderer is not safe
// for concurrent use; the UI drives it from render commands one at a time.
type Renderer struct {
	width int
	style string
	md    *glamour.TermRenderer
}

// New builds a Renderer wrapping a Glamour terminal renderer at the given width.
// The Glamour style is chosen from the terminal background (dark vs light) so
// output is legible on either; glamour/v2 dropped v1's WithAutoStyle, so the
// choice is made explicitly here.
func New(width int) (*Renderer, error) {
	r := &Renderer{width: width, style: detectStyle()}
	if err := r.rebuild(); err != nil {
		return nil, err
	}
	return r, nil
}

// detectStyle returns the Glamour standard style matching the terminal
// background. It defaults to the light style when the background cannot be
// determined (e.g. no TTY in tests), which keeps output deterministic.
func detectStyle() string {
	return styleForBackground(lipgloss.HasDarkBackground(os.Stdin, os.Stdout))
}

// styleForBackground maps a dark-background flag to the Glamour standard style
// name. Split out from detectStyle so the mapping is unit-testable without a TTY.
func styleForBackground(dark bool) string {
	if dark {
		return styles.DarkStyle
	}
	return styles.LightStyle
}

// rebuild constructs a fresh Glamour renderer for the current width and style.
func (r *Renderer) rebuild() error {
	md, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(r.style),
		glamour.WithWordWrap(r.width),
	)
	if err != nil {
		return fmt.Errorf("new glamour renderer: %w", err)
	}
	r.md = md
	return nil
}

// SetWidth rebuilds the underlying Glamour renderer to wrap at w columns. It is a
// no-op when the width is unchanged or non-positive, so resize spam is cheap.
func (r *Renderer) SetWidth(w int) error {
	if w <= 0 || w == r.width {
		return nil
	}
	r.width = w
	return r.rebuild()
}

// Render converts an item's body to terminal-ready styled text. When BodyHTML is
// present it is converted to markdown (which sanitizes away scripts/styles) and
// then styled; otherwise the plain BodyText is styled directly. Glamour wraps
// both to the configured width.
func (r *Renderer) Render(it model.Item) (string, error) {
	if strings.TrimSpace(it.BodyHTML) == "" {
		out, err := r.md.Render(it.BodyText)
		if err != nil {
			return "", fmt.Errorf("render text: %w", err)
		}
		return out, nil
	}
	md, err := htmltomarkdown.ConvertString(it.BodyHTML)
	if err != nil {
		return "", fmt.Errorf("html->md: %w", err)
	}
	out, err := r.md.Render(md)
	if err != nil {
		return "", fmt.Errorf("render markdown: %w", err)
	}
	return out, nil
}
