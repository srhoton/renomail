// Package keys defines the application key bindings and the help.KeyMap that
// drives the on-screen help line (DESIGN.md §6.7). Step 04 wired the navigation,
// open/back, help, and quit keys; step 05 adds search, the quick filters, and the
// read-state toggles. The sync keys arrive in a later step.
package keys

import "charm.land/bubbles/v2/key"

// KeyMap holds every binding the TUI reacts to. It implements help.KeyMap so the
// bubbles/help component can render the short and full help views from it.
//
// Up/Down/Top/Bottom are listed for the help view, but motion is handled by the
// embedded bubbles/list (which binds j/k/g/G itself); the root model's handleKey
// only matches Open/Back/Help/Quit and the filter/read keys, and lets everything
// else fall through to the active child. The bindings are kept here so the
// displayed help stays accurate.
type KeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Top    key.Binding
	Bottom key.Binding
	Open   key.Binding
	Back   key.Binding
	Quit   key.Binding
	// ForceQuit (ctrl+c only) quits even from contexts where the plain Quit key
	// ("q") must be typeable, such as the search bar.
	ForceQuit key.Binding
	Help      key.Binding

	// Filtering and read-state (step 05).
	Search       key.Binding // "/"  open the search bar
	FilterEmail  key.Binding // "e"  scope to email
	FilterRSS    key.Binding // "r"  scope to rss
	FilterUnread key.Binding // "u"  show unread only
	FilterAll    key.Binding // "a"  reset every filter
	ToggleRead   key.Binding // "m"  toggle the selected item's read flag
	MarkAllRead  key.Binding // "M"  mark every matching item read
}

// Default returns the standard key bindings: vi-style motion, open/back/help/quit,
// plus the search, quick-filter, and read-state keys.
func Default() KeyMap {
	return KeyMap{
		Up:        key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k/↑", "up")),
		Down:      key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/↓", "down")),
		Top:       key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
		Bottom:    key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom")),
		Open:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Back:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		ForceQuit: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),

		Search:       key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
		FilterEmail:  key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "email")),
		FilterRSS:    key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "rss")),
		FilterUnread: key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "unread")),
		FilterAll:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "all")),
		ToggleRead:   key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "toggle read")),
		MarkAllRead:  key.NewBinding(key.WithKeys("M"), key.WithHelp("M", "mark all read")),
	}
}

// ShortHelp returns the compact help shown at the bottom of the feed view.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Open, k.Search, k.ToggleRead, k.Help, k.Quit}
}

// FullHelp returns the expanded help, grouped into columns, shown when help is
// toggled open.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Top, k.Bottom},
		{k.Open, k.Back},
		{k.Search, k.FilterEmail, k.FilterRSS, k.FilterUnread, k.FilterAll},
		{k.ToggleRead, k.MarkAllRead},
		{k.Help, k.Quit},
	}
}
