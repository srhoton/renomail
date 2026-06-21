// Package filterbar provides the search input shown in the filter view: a thin
// wrapper over bubbles/textinput that the root model focuses when the user presses
// "/" and reads back when they press Enter (DESIGN.md §6.6). Quick-filter toggles
// (e/r/u/a) and read-state keys are handled by the root model directly; this
// package owns only the free-text search term.
package filterbar

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// prompt is the leading marker shown before the search term, echoing the "/" key
// that opens the bar.
const prompt = "/ "

// Model wraps a single-line text input used to edit the feed's search term.
type Model struct {
	input textinput.Model
}

// New builds an unfocused search input with the "/ " prompt.
func New() Model {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.Placeholder = "search title, author, body…"
	return Model{input: ti}
}

// Focus gives the input keyboard focus and returns the command that starts its
// cursor blinking.
func (m *Model) Focus() tea.Cmd { return m.input.Focus() }

// Blur removes keyboard focus from the input.
func (m *Model) Blur() { m.input.Blur() }

// Focused reports whether the input currently has keyboard focus.
func (m Model) Focused() bool { return m.input.Focused() }

// Value returns the current search term.
func (m Model) Value() string { return m.input.Value() }

// SetValue replaces the current search term, e.g. to pre-fill the bar with the
// active filter's term when reopening it.
func (m *Model) SetValue(s string) { m.input.SetValue(s) }

// Update forwards a message (keystrokes, cursor blink) to the embedded input.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// View renders the prompt and the current input.
func (m Model) View() string { return m.input.View() }
