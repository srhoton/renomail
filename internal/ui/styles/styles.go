// Package styles holds the lipgloss theme shared across the TUI views: the feed
// list rows (read vs unread), the reader header, and the status bar
// (DESIGN.md §6.4).
package styles

import (
	"os"

	"charm.land/lipgloss/v2"
)

// Styles is the set of lipgloss styles the views render with. It is passed by
// value into the feed and reader models so a single theme drives the whole UI.
type Styles struct {
	Unread    lipgloss.Style // bright/bold feed rows for unread items
	Read      lipgloss.Style // dimmed feed rows for already-read items
	Selected  lipgloss.Style // background highlight for the feed row under the cursor
	Header    lipgloss.Style // the reader's From/Title/Source/date header
	StatusBar lipgloss.Style // the bottom status/help line
}

// DefaultStyles returns the built-in theme, choosing background-aware colors from
// the detected terminal background so the reader header stays legible on both
// light and dark terminals. Unread rows are bold/bright and read rows are
// faint/dimmed, so scanning the feed surfaces what is new.
func DefaultStyles() Styles {
	return stylesForBackground(lipgloss.HasDarkBackground(os.Stdin, os.Stdout))
}

// stylesForBackground builds the theme for a given background brightness. It is
// split out from DefaultStyles so both the light and dark palettes are testable
// without a real terminal. Bold/Faint are background-independent; only the header
// accent color is picked per background (a deeper purple on light, a brighter one
// on dark) for contrast against the terminal's default text color.
func stylesForBackground(dark bool) Styles {
	lightDark := lipgloss.LightDark(dark)
	header := lightDark(lipgloss.Color("63"), lipgloss.Color("141"))
	// A muted gray bar so the selected line stands out without fighting the text
	// colors. 252 and 238 are neighbours on the xterm-256 grayscale ramp (232=near
	// black … 255=near white): a light gray on light terminals, a dark gray on dark
	// ones — just off the terminal's default background so the bar reads as a
	// highlight while bold/faint text stays legible on top.
	selBg := lightDark(lipgloss.Color("252"), lipgloss.Color("238"))
	return Styles{
		Unread:    lipgloss.NewStyle().Bold(true),
		Read:      lipgloss.NewStyle().Faint(true),
		Selected:  lipgloss.NewStyle().Background(selBg),
		Header:    lipgloss.NewStyle().Bold(true).Foreground(header),
		StatusBar: lipgloss.NewStyle().Faint(true),
	}
}

// RowStyle returns the style for a feed row: the bold (unread) / faint (read) base,
// with the selection background layered on for the cursor line so the whole row
// reads as a highlighted bar while keeping its emphasis.
func (s Styles) RowStyle(read, selected bool) lipgloss.Style {
	base := s.Unread
	if read {
		base = s.Read
	}
	if selected {
		base = base.Background(s.Selected.GetBackground())
	}
	return base
}
