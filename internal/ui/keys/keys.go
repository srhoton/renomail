// Package keys defines the application key bindings and the help.KeyMap that
// drives the on-screen help line (DESIGN.md §6.7). Step 04 wires the navigation,
// open/back, help, and quit keys; the filter/read/sync keys arrive in later steps.
package keys

import "charm.land/bubbles/v2/key"

// KeyMap holds every binding the TUI reacts to. It implements help.KeyMap so the
// bubbles/help component can render the short and full help views from it.
//
// Up/Down/Top/Bottom are listed for the help view, but motion is handled by the
// embedded bubbles/list (which binds j/k/g/G itself); the root model's handleKey
// only matches Open/Back/Help/Quit and lets everything else fall through to the
// active child. The bindings are kept here so the displayed help stays accurate.
type KeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Top    key.Binding
	Bottom key.Binding
	Open   key.Binding
	Back   key.Binding
	Quit   key.Binding
	Help   key.Binding
}

// Default returns the standard key bindings: vi-style motion plus the common
// open/back/help/quit keys.
func Default() KeyMap {
	return KeyMap{
		Up:     key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k/↑", "up")),
		Down:   key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/↓", "down")),
		Top:    key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
		Bottom: key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom")),
		Open:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Back:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	}
}

// ShortHelp returns the compact help shown at the bottom of the feed view.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Open, k.Back, k.Help, k.Quit}
}

// FullHelp returns the expanded help, grouped into columns, shown when help is
// toggled open.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Top, k.Bottom},
		{k.Open, k.Back},
		{k.Help, k.Quit},
	}
}
