// Package ui hosts the root Bubble Tea model that routes between the feed list and
// the reader view, owns the store and render pipeline, and turns key presses into
// store-backed commands (DESIGN.md §6). It targets the Charm v2 stack: View
// returns a tea.View (alt-screen set on it) and key presses arrive as
// tea.KeyPressMsg.
package ui

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/render"
	"github.com/srhoton/renomail/internal/store"
	"github.com/srhoton/renomail/internal/ui/feed"
	"github.com/srhoton/renomail/internal/ui/keys"
	"github.com/srhoton/renomail/internal/ui/reader"
	"github.com/srhoton/renomail/internal/ui/styles"
)

// defaultWidth is the renderer's starting wrap width before the first
// WindowSizeMsg arrives with the real terminal size.
const defaultWidth = 80

// view enumerates the routable screens. viewFilter and viewHelp are reserved for
// later steps; step 04 routes only between the feed and reader.
type view int

const (
	viewFeed view = iota
	viewReader
	viewFilter
	viewHelp
)

// Model is the root application model. It routes messages to the active child
// view and owns the shared store, render pipeline, key map, and help.
type Model struct {
	view     view
	feed     feed.Model
	reader   reader.Model
	filter   model.Filter
	store    *store.Store
	renderer *render.Renderer
	keys     keys.KeyMap
	help     help.Model
	theme    styles.Styles
	openID   string // id of the item currently open in the reader (stale-load guard)
	status   string
	w, h     int
}

// compile-time check that Model satisfies the Bubble Tea contract.
var _ tea.Model = Model{}

// New builds the root model over an open store. It initializes the render
// pipeline, the feed and reader views, the key map, and help.
func New(st *store.Store) (Model, error) {
	r, err := render.New(defaultWidth)
	if err != nil {
		return Model{}, err
	}
	theme := styles.DefaultStyles()
	return Model{
		view:     viewFeed,
		feed:     feed.New(theme),
		reader:   reader.New(theme),
		filter:   model.Filter{},
		store:    st,
		renderer: r,
		keys:     keys.Default(),
		help:     help.New(),
		theme:    theme,
	}, nil
}

// Init loads the cached items into the feed immediately, so the UI shows content
// without waiting on any network sync.
func (m Model) Init() tea.Cmd { return loadItemsCmd(m.store, m.filter) }

// Update handles framework and application messages, then routes the remainder to
// the active child view.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		_ = m.renderer.SetWidth(msg.Width)
		m.help.SetWidth(msg.Width)
		childH := max(msg.Height-1, 1) // leave a row for the status/help line
		m.feed.SetSize(msg.Width, childH)
		m.reader.SetSize(msg.Width, childH)
		return m, nil
	case itemsLoadedMsg:
		return m, m.feed.SetItems(msg.items)
	case bodyLoadedMsg:
		// Ignore a body that finished rendering after the user opened a different
		// item (or none): only the currently-open item's body belongs in the
		// reader. This prevents a slow render from clobbering newer content.
		if msg.id != m.openID {
			return m, nil
		}
		if msg.err != nil {
			m.status = msg.err.Error()
			return m, nil
		}
		m.reader.SetContent(msg.rendered)
		return m, nil
	case errMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m.routeToChild(msg)
}

// handleKey applies the key bindings for the active view: open/back routing, help
// toggle, and quit. Unhandled keys fall through to the active child (list motion
// or viewport scrolling).
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Quit) {
		return m, tea.Quit
	}

	switch m.view {
	case viewFeed:
		switch {
		case key.Matches(msg, m.keys.Help):
			m.help.ShowAll = !m.help.ShowAll
			return m, nil
		case key.Matches(msg, m.keys.Open):
			it, ok := m.feed.Selected()
			if !ok {
				return m, nil
			}
			m.openID = it.ID
			m.reader.SetHeader(it)
			// Clear any previous body so the prior item does not flash under the
			// new header while this item's body renders asynchronously.
			m.reader.SetContent("")
			m.view = viewReader
			cmds := []tea.Cmd{loadBodyCmd(m.store, m.renderer, it)}
			// Opening an item marks it read (DESIGN §6.5): flip the row now so the
			// dot updates on return, and persist the flag in the background.
			if !it.Read {
				m.feed.MarkSelectedRead()
				cmds = append(cmds, setReadCmd(m.store, it.ID, true))
			}
			return m, tea.Batch(cmds...)
		}
	case viewReader:
		if key.Matches(msg, m.keys.Back) {
			m.view = viewFeed
			return m, nil
		}
	}
	return m.routeToChild(msg)
}

// routeToChild forwards a message to the active child view and returns the
// updated model.
func (m Model) routeToChild(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.view {
	case viewReader:
		m.reader, cmd = m.reader.Update(msg)
	default:
		m.feed, cmd = m.feed.Update(msg)
	}
	return m, cmd
}

// View renders the active view into a tea.View with the alternate screen enabled
// (the v2 replacement for the WithAltScreen program option).
func (m Model) View() tea.View {
	var content string
	switch m.view {
	case viewReader:
		content = m.reader.View()
	default:
		content = m.feed.View() + "\n" + m.statusBar()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// statusBar renders the help line, prefixed with the last status/error message
// when present. It uses the theme cached on the model rather than rebuilding it.
func (m Model) statusBar() string {
	helpLine := m.help.View(m.keys)
	if m.status != "" {
		return m.theme.StatusBar.Render(m.status) + "  " + helpLine
	}
	return helpLine
}
