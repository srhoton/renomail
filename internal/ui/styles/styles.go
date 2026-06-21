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
	return Styles{
		Unread:    lipgloss.NewStyle().Bold(true),
		Read:      lipgloss.NewStyle().Faint(true),
		Header:    lipgloss.NewStyle().Bold(true).Foreground(header),
		StatusBar: lipgloss.NewStyle().Faint(true),
	}
}
