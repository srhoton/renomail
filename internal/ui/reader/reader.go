// Package reader renders an opened item in a scrollable viewport with a styled
// header showing From/Title/Source/date (DESIGN.md §6.5). The body it displays is
// produced by the render pipeline; this package only frames and scrolls it.
package reader

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/ui/styles"
)

// headerLines is the number of rows the header occupies (title, meta, blank
// separator); the viewport gets the rest of the screen.
const headerLines = 3

// Model is the reader view: a header line plus a scrollable viewport for the
// rendered body.
type Model struct {
	vp     viewport.Model
	header string
	styles styles.Styles
	w, h   int
}

// New builds an empty reader with the given theme.
func New(st styles.Styles) Model {
	return Model{vp: viewport.New(), styles: st}
}

// SetSize lays out the viewport beneath the header for the available area.
func (m *Model) SetSize(w, h int) {
	m.w, m.h = w, h
	m.vp.SetWidth(w)
	m.vp.SetHeight(max(h-headerLines, 1))
}

// SetHeader builds the styled header from the item's metadata. Author and date
// are omitted when absent so the line stays clean.
func (m *Model) SetHeader(it model.Item) {
	title := m.styles.Header.Render(it.Title)

	meta := it.SourceName
	if it.Author != "" {
		meta = it.Author + " · " + meta
	}
	if !it.Published.IsZero() {
		meta += " · " + it.Published.Format("2006-01-02 15:04")
	}
	m.header = m.styles.StatusBar.Render(meta)
	m.header = title + "\n" + m.header
}

// SetContent loads the rendered body into the viewport and scrolls back to the
// top so each opened item starts at its beginning.
func (m *Model) SetContent(rendered string) {
	m.vp.SetContent(strings.TrimRight(rendered, "\n"))
	m.vp.GotoTop()
}

// Update forwards scroll messages to the viewport.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// View renders the header above the scrollable body.
func (m Model) View() string {
	return fmt.Sprintf("%s\n\n%s", m.header, m.vp.View())
}
