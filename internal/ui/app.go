// Package ui hosts the root Bubble Tea model that routes between the feed list and
// the reader view, owns the store and render pipeline, and turns key presses into
// store-backed commands (DESIGN.md §6). It targets the Charm v2 stack: View
// returns a tea.View (alt-screen set on it) and key presses arrive as
// tea.KeyPressMsg.
package ui

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/render"
	"github.com/srhoton/renomail/internal/store"
	"github.com/srhoton/renomail/internal/ui/feed"
	"github.com/srhoton/renomail/internal/ui/filterbar"
	"github.com/srhoton/renomail/internal/ui/keys"
	"github.com/srhoton/renomail/internal/ui/reader"
	"github.com/srhoton/renomail/internal/ui/styles"
)

// defaultWidth is the renderer's starting wrap width before the first
// WindowSizeMsg arrives with the real terminal size.
const defaultWidth = 80

// view enumerates the routable screens. viewHelp is reserved for a later step;
// step 04 routed feed↔reader, and step 05 adds viewFilter for the search bar.
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
	view      view
	feed      feed.Model
	reader    reader.Model
	filterbar filterbar.Model
	filter    model.Filter
	store     *store.Store
	renderer  *render.Renderer
	keys      keys.KeyMap
	help      help.Model
	theme     styles.Styles
	openID    string // id of the item currently open in the reader (stale-load guard)
	status    string
	hint      string // cached status-bar filter description; refreshed only on filter change
	w, h      int
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
		view:      viewFeed,
		feed:      feed.New(theme),
		reader:    reader.New(theme),
		filterbar: filterbar.New(),
		filter:    model.Filter{},
		store:     st,
		renderer:  r,
		keys:      keys.Default(),
		help:      help.New(),
		theme:     theme,
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
	case readToggledMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return m, nil
		}
		// Under an unread-only filter a now-read item must leave the list, so
		// re-query; otherwise confirm the flag in place (a no-op when the caller
		// already flipped it optimistically).
		if msg.read && m.filter.Read == model.ReadUnreadOnly {
			return m, loadItemsCmd(m.store, m.filter)
		}
		m.feed.SetReadLocal(msg.id, msg.read)
		return m, nil
	case reloadMsg:
		return m, loadItemsCmd(m.store, m.filter)
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m.routeToChild(msg)
}

// handleKey applies the key bindings for the active view. The filter view is
// dispatched first, before the global quit binding, so a literal "q" typed into
// the search box edits the term instead of quitting (only ctrl+c quits while
// editing). Unhandled keys fall through to the active child (list motion or
// viewport scrolling).
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.view == viewFilter {
		return m.handleFilterKey(msg)
	}
	if key.Matches(msg, m.keys.Quit) {
		return m, tea.Quit
	}

	switch m.view {
	case viewFeed:
		return m.handleFeedKey(msg)
	case viewReader:
		if key.Matches(msg, m.keys.Back) {
			m.view = viewFeed
			return m, nil
		}
	}
	return m.routeToChild(msg)
}

// handleFeedKey handles the feed view's bindings: search, the quick filters, the
// read-state toggles, help, and opening an item. Each filter change rebuilds the
// filter and re-runs the single query path so the visible feed and the SQL stay
// in lockstep (DESIGN §6.6).
func (m Model) handleFeedKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		return m, nil
	case key.Matches(msg, m.keys.Search):
		// Pre-fill the bar with the active term so the user edits rather than
		// retypes, then focus it.
		m.filterbar.SetValue(m.filter.Search)
		m.view = viewFilter
		return m, m.filterbar.Focus()
	case key.Matches(msg, m.keys.FilterEmail):
		m.filter.Kinds = map[model.Kind]bool{model.KindEmail: true}
		return m, m.applyFilter()
	case key.Matches(msg, m.keys.FilterRSS):
		m.filter.Kinds = map[model.Kind]bool{model.KindRSS: true}
		return m, m.applyFilter()
	case key.Matches(msg, m.keys.FilterUnread):
		m.filter.Read = model.ReadUnreadOnly
		return m, m.applyFilter()
	case key.Matches(msg, m.keys.FilterAll):
		m.filter = model.Filter{}
		return m, m.applyFilter()
	case key.Matches(msg, m.keys.ToggleRead):
		it, ok := m.feed.Selected()
		if !ok {
			return m, nil
		}
		next := !it.Read
		m.feed.SetReadLocal(it.ID, next) // optimistic; readToggledMsg confirms
		return m, setReadCmd(m.store, it.ID, next)
	case key.Matches(msg, m.keys.MarkAllRead):
		return m, markAllReadCmd(m.store, m.filter)
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
		// Opening an item marks it read (DESIGN §6.5): flip the row now (by id, the
		// same stale-safe path used everywhere else) so the dot updates on return,
		// and persist the flag in the background.
		if !it.Read {
			m.feed.SetReadLocal(it.ID, true)
			cmds = append(cmds, setReadCmd(m.store, it.ID, true))
		}
		return m, tea.Batch(cmds...)
	}
	return m.routeToChild(msg)
}

// handleFilterKey handles the search bar: ctrl+c still quits, Esc cancels (keeping
// the previous filter), Enter applies the typed term and re-queries, and every
// other key edits the term.
func (m Model) handleFilterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.ForceQuit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Back):
		m.view = viewFeed
		m.filterbar.Blur()
		return m, nil
	case key.Matches(msg, m.keys.Open):
		m.filter.Search = strings.TrimSpace(m.filterbar.Value())
		m.view = viewFeed
		m.filterbar.Blur()
		return m, m.applyFilter()
	}
	var cmd tea.Cmd
	m.filterbar, cmd = m.filterbar.Update(msg)
	return m, cmd
}

// applyFilter refreshes the cached status-bar hint to match the current filter and
// returns the command that re-runs the query. Centralizing it keeps the per-frame
// statusBar free of recomputation: the hint string changes only on a filter change
// (a per-keystroke event at most), never per render.
func (m *Model) applyFilter() tea.Cmd {
	m.hint = filterHint(m.filter)
	return loadItemsCmd(m.store, m.filter)
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
// (the v2 replacement for the WithAltScreen program option). In the filter view
// the search bar replaces the status line so the user sees what they type.
func (m Model) View() tea.View {
	var content string
	switch m.view {
	case viewReader:
		content = m.reader.View()
	case viewFilter:
		content = m.feed.View() + "\n" + m.filterbar.View()
	default:
		content = m.feed.View() + "\n" + m.statusBar()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// statusBar renders the help line, prefixed with the active filter hint and the
// last status/error message when present. It uses the theme cached on the model
// rather than rebuilding it.
func (m Model) statusBar() string {
	helpLine := m.help.View(m.keys)
	left := m.status
	if m.hint != "" {
		if left != "" {
			left += "  "
		}
		left += "Filter: " + m.hint
	}
	if left == "" {
		return helpLine
	}
	return m.theme.StatusBar.Render(left) + "  " + helpLine
}

// filterHint renders the active filter compactly, e.g. `rss · unread · "k8s"`, so
// the current scope is always visible. A zero filter yields the empty string. It
// is pure and computed only on filter change (see applyFilter), never per frame.
func filterHint(f model.Filter) string {
	var parts []string
	if f.Kinds[model.KindEmail] {
		parts = append(parts, string(model.KindEmail))
	}
	if f.Kinds[model.KindRSS] {
		parts = append(parts, string(model.KindRSS))
	}
	switch f.Read {
	case model.ReadUnreadOnly:
		parts = append(parts, "unread")
	case model.ReadReadOnly:
		parts = append(parts, "read")
	case model.ReadAny:
		// no hint
	}
	if f.Search != "" {
		parts = append(parts, `"`+f.Search+`"`)
	}
	return strings.Join(parts, " · ")
}
