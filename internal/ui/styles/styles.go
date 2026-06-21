// Package styles holds the lipgloss theme shared across the TUI views: the feed
// list rows (read vs unread), the reader header, and the status bar
// (DESIGN.md §6.4).
package styles

import "charm.land/lipgloss/v2"

// Styles is the set of lipgloss styles the views render with. It is passed by
// value into the feed and reader models so a single theme drives the whole UI.
type Styles struct {
	Unread    lipgloss.Style // bright/bold feed rows for unread items
	Read      lipgloss.Style // dimmed feed rows for already-read items
	Header    lipgloss.Style // the reader's From/Title/Source/date header
	StatusBar lipgloss.Style // the bottom status/help line
}

// DefaultStyles returns the built-in theme: unread rows are bold and bright,
// read rows are faint/dimmed, so scanning the feed surfaces what is new.
func DefaultStyles() Styles {
	return Styles{
		Unread:    lipgloss.NewStyle().Bold(true),
		Read:      lipgloss.NewStyle().Faint(true),
		Header:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")),
		StatusBar: lipgloss.NewStyle().Faint(true),
	}
}
