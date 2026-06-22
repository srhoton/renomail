// Package ui hosts the root Bubble Tea model that routes between the feed list and
// the reader view, owns the store and render pipeline, and turns key presses into
// store-backed commands (DESIGN.md §6). It targets the Charm v2 stack: View
// returns a tea.View (alt-screen set on it) and key presses arrive as
// tea.KeyPressMsg.
package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/pkg/browser"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/render"
	"github.com/srhoton/renomail/internal/source"
	"github.com/srhoton/renomail/internal/store"
	"github.com/srhoton/renomail/internal/syncengine"
	"github.com/srhoton/renomail/internal/ui/feed"
	"github.com/srhoton/renomail/internal/ui/filterbar"
	"github.com/srhoton/renomail/internal/ui/keys"
	"github.com/srhoton/renomail/internal/ui/reader"
	"github.com/srhoton/renomail/internal/ui/styles"
)

// defaultWidth is the renderer's starting wrap width before the first
// WindowSizeMsg arrives with the real terminal size.
const defaultWidth = 80

// view enumerates the routable screens: the feed list, the reader, and the search
// filter bar. Help is an in-place toggle on the feed (m.help.ShowAll), not a
// separate screen.
type view int

const (
	viewFeed view = iota
	viewReader
	viewFilter
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
	openID    string     // id of the item currently open in the reader (stale-load guard)
	openItem  model.Item // the item currently open in the reader (for resize re-render / open-in-browser)
	status    string
	hint      string // cached status-bar filter description; refreshed only on filter change
	helpView  string // cached help line; refreshed only on resize / help toggle
	w, h      int

	// actions (step 08)
	openURL     func(string) error // opens an item's permalink; injectable for tests (default browser.OpenURL)
	triggerSync func()             // requests an out-of-band engine sweep; nil ⇒ no-op
	notify      func(string) error // pushes a host notification (tmux) on new items; default no-op

	// background sync
	events    <-chan syncengine.Result   // engine results, drained by waitForActivity
	providers map[string]source.Provider // by source ID, for body-on-open fallback
	hydrated  map[string]bool            // item ids whose body was loaded this session
	spinner   spinner.Model              // shown while the initial sweep is in flight
	lastSync  time.Time                  // when the most recent sync result arrived
	syncing   bool                       // true until the initial sweep completes
	inflight  int                        // providers still to report in the initial sweep
	sources   int                        // total provider count, for the status indicator
}

// compile-time check that Model satisfies the Bubble Tea contract.
var _ tea.Model = Model{}

// New builds the root model over an open store, the background engine's events
// channel, and the provider set. It initializes the render pipeline, the feed and
// reader views, the key map, help, and the sync spinner. The providers are indexed
// by source ID so the reader can fall back to a network body load on open; warns
// (e.g. un-authorized Gmail accounts from provider construction) seed the status
// line so the user sees them at startup.
func New(
	st *store.Store,
	events <-chan syncengine.Result,
	providers []source.Provider,
	triggerSync func(),
	warns []error,
) (Model, error) {
	r, err := render.New(defaultWidth)
	if err != nil {
		return Model{}, err
	}
	theme := styles.DefaultStyles()

	byID := make(map[string]source.Provider, len(providers))
	for _, p := range providers {
		byID[p.ID()] = p
	}

	m := Model{
		view:        viewFeed,
		feed:        feed.New(theme),
		reader:      reader.New(theme),
		filterbar:   filterbar.New(),
		filter:      model.Filter{},
		store:       st,
		renderer:    r,
		keys:        keys.Default(),
		help:        help.New(),
		theme:       theme,
		status:      warnStatus(warns),
		events:      events,
		providers:   byID,
		hydrated:    make(map[string]bool),
		spinner:     spinner.New(),
		sources:     len(providers),
		openURL:     browser.OpenURL,
		triggerSync: triggerSync,
		// Default to a no-op; the cmd layer swaps in notify.Tmux only when running
		// inside tmux with notifications enabled, keeping this package free of any
		// environment/process concerns.
		notify: func(string) error { return nil },
		// The engine runs an immediate first sweep emitting one result per
		// provider; seed inflight with that count so the spinner runs until the
		// initial sweep completes. With no providers there is nothing to wait for.
		syncing:  len(providers) > 0,
		inflight: len(providers),
	}
	m.refreshHelp()
	return m, nil
}

// SetNotifier installs the host-notification function invoked when new items
// arrive during a steady-state sync (e.g. notify.Tmux). The cmd layer wires it
// only when running inside a supported multiplexer with notifications enabled; a
// nil argument is ignored so the model keeps its no-op default.
func (m *Model) SetNotifier(fn func(string) error) {
	if fn != nil {
		m.notify = fn
	}
}

// refreshHelp recomputes the cached help line. It is called only when the inputs
// change — the help width (on resize) or the expanded/collapsed state (on toggle)
// — so the per-frame statusBar never rebuilds it.
func (m *Model) refreshHelp() { m.helpView = m.help.View(m.keys) }

// warnStatus renders provider-construction warnings into a compact startup status
// string (e.g. an un-authorized Gmail account prompting `renomail auth`). The
// empty string when there are none keeps the status line clean.
func warnStatus(warns []error) string {
	if len(warns) == 0 {
		return ""
	}
	msgs := make([]string, 0, len(warns))
	for _, w := range warns {
		msgs = append(msgs, w.Error())
	}
	return strings.Join(msgs, "; ")
}

// Init loads the cached items into the feed immediately (so the UI shows content
// without waiting on any network sync), begins draining the engine's results, and
// starts the spinner for the initial sweep. inflight is seeded with the provider
// count so the spinner stops once every provider has reported once.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		loadItemsCmd(m.store, m.filter),
		waitForActivity(m.events),
		m.spinner.Tick,
	)
}

// Update handles framework and application messages, then routes the remainder to
// the active child view.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		_ = m.renderer.SetWidth(msg.Width)
		m.help.SetWidth(msg.Width)
		m.refreshHelp()
		childH := max(msg.Height-1, 1) // leave a row for the status/help line
		m.feed.SetSize(msg.Width, childH)
		m.reader.SetSize(msg.Width, childH)
		// Re-render the open item at the new width so the reader re-wraps instead of
		// keeping the previous width's layout. The body is already cached (the item is
		// open), so a nil provider reads straight from the store — no network fetch.
		if m.view == viewReader && m.openID != "" {
			return m, loadBodyCmd(m.store, m.renderer, nil, m.openItem)
		}
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
		// Mark the item hydrated for this session so re-opening it renders straight
		// from the store cache instead of re-hitting the provider — important for
		// genuinely body-less mail, whose empty cached body is otherwise
		// indistinguishable from "never fetched".
		m.hydrated[msg.id] = true
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
		// When a read-state filter is active, a toggle that moves the item out of
		// the filtered set must remove it from the list; drop just that row in
		// place rather than re-querying the whole feed (only this one item
		// changed). Otherwise confirm the flag in place (a no-op when the caller
		// already flipped it optimistically).
		leaves := (m.filter.Read == model.ReadUnreadOnly && msg.read) ||
			(m.filter.Read == model.ReadReadOnly && !msg.read)
		if leaves {
			m.feed.RemoveLocal(msg.id)
			return m, nil
		}
		m.feed.SetReadLocal(msg.id, msg.read)
		return m, nil
	case reloadMsg:
		return m, loadItemsCmd(m.store, m.filter)
	case spinner.TickMsg:
		// Only advance the spinner while a sweep is in flight; once it stops the
		// tick loop ends (we stop returning the next tick command), so the status
		// bar settles on the "synced N ago" indicator.
		if !m.syncing {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case syncBatchMsg:
		return m.handleSyncBatch(msg)
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m.routeToChild(msg)
}

// handleSyncBatch folds one provider's sweep result into the model: it records the
// sync time, surfaces a fetch error to the status line (without crashing), and
// re-queries the feed when new items arrived (the engine already upserted them). It
// tracks the initial sweep's progress so the spinner stops once every provider has
// reported, and always re-arms the listener so the results stream keeps draining.
//
// During the initial sweep the per-provider re-queries are coalesced into a single
// query once the whole sweep has reported, rather than one full table scan per
// provider; steady-state ticks (a single provider reporting) re-query inline.
func (m Model) handleSyncBatch(msg syncBatchMsg) (tea.Model, tea.Cmd) {
	r := msg.res
	m.lastSync = time.Now()
	initialSweep := m.inflight > 0
	if m.inflight > 0 {
		m.inflight--
		if m.inflight == 0 {
			m.syncing = false
		}
	}

	cmds := []tea.Cmd{waitForActivity(m.events)}
	if r.Err != nil {
		m.status = fmt.Sprintf("sync %s: %v", r.SourceName, r.Err)
	}
	switch {
	case initialSweep && m.inflight == 0:
		// Whole initial sweep has reported: one re-query covers every provider's items.
		cmds = append(cmds, loadItemsCmd(m.store, m.filter))
	case !initialSweep && len(r.Items) > 0:
		cmds = append(cmds, loadItemsCmd(m.store, m.filter))
	}
	// Notify on genuinely new items arriving in steady state. The initial sweep is
	// suppressed (on first launch everything looks new, and the user is already
	// looking at the app); m.notify is a no-op unless we are running inside tmux.
	if !initialSweep && r.Inserted > 0 {
		cmds = append(cmds, notifyCmd(m.notify, r.SourceName, r.Inserted))
	}
	return m, tea.Batch(cmds...)
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
	// Any key outside the search bar dismisses a stale transient status (a sync or
	// body error), so it does not linger; a fresh error arriving later re-sets it.
	m.status = ""
	if key.Matches(msg, m.keys.Quit) {
		return m, tea.Quit
	}
	// Actions available from both the feed and the reader.
	switch {
	case key.Matches(msg, m.keys.OpenBrowser):
		return m, m.openInBrowser()
	case key.Matches(msg, m.keys.ForceSync):
		return m.forceSync()
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

// current returns the item the user is acting on: the open item while in the
// reader, otherwise the selected feed row. The bool is false when there is none
// (e.g. an empty feed).
//
// In the reader it returns m.openItem, a snapshot taken at open time. Its
// immutable fields (ID, URL) are always current, but mutable fields such as Read
// may lag the feed's optimistic flips — callers must not key off them. Today only
// the URL is read (open-in-browser), so this is safe.
func (m Model) current() (model.Item, bool) {
	if m.view == viewReader && m.openID != "" {
		return m.openItem, true
	}
	return m.feed.Selected()
}

// openInBrowser returns a command that opens the current item's permalink in the
// system browser, surfacing any failure on the status line. It is a no-op (nil
// command) when there is no current item or it has no URL.
func (m Model) openInBrowser() tea.Cmd {
	it, ok := m.current()
	if !ok || it.URL == "" {
		return nil
	}
	open, u := m.openURL, it.URL
	return func() tea.Msg {
		if err := open(u); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// forceSync requests an immediate engine sweep and re-lights the spinner for it,
// reusing the inflight counter the sync-batch handler already drains. It is a
// no-op while a sweep is already running (so inflight is not double-counted) or
// when there are no sources / no engine wired.
func (m Model) forceSync() (tea.Model, tea.Cmd) {
	if m.syncing || m.triggerSync == nil || m.sources == 0 {
		return m, nil
	}
	m.triggerSync()
	m.syncing = true
	m.inflight = m.sources
	return m, m.spinner.Tick
}

// handleFeedKey handles the feed view's bindings: search, the quick filters, the
// read-state toggles, help, and opening an item. Each filter change rebuilds the
// filter and re-runs the single query path so the visible feed and the SQL stay
// in lockstep (DESIGN §6.6).
func (m Model) handleFeedKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.refreshHelp()
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
	case key.Matches(msg, m.keys.CycleRead):
		m.filter.Read = m.filter.Read.Next()
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
	case key.Matches(msg, m.keys.MarkSourceRead):
		it, ok := m.feed.Selected()
		if !ok || it.SourceID == "" {
			return m, nil
		}
		// Mark the whole source read regardless of the active filter: a fresh
		// source-only filter, not m.filter. The reload (reloadMsg) then re-queries
		// with the current filter, so the source's rows dim — or drop, under an
		// unread-only filter.
		//
		// Set a transient confirmation: with no filter active the only visible effect
		// is the rows dimming. handleKey already cleared the prior status before
		// dispatch, so this survives until the next keypress.
		m.status = "marked " + sourceLabel(it) + " read"
		return m, markAllReadCmd(m.store, model.Filter{SourceIDs: map[string]bool{it.SourceID: true}})
	case key.Matches(msg, m.keys.Open):
		it, ok := m.feed.Selected()
		if !ok {
			return m, nil
		}
		m.openID = it.ID
		m.openItem = it // retained for resize re-render and open-in-browser
		m.reader.SetHeader(it)
		// Clear any previous body so the prior item does not flash under the
		// new header while this item's body renders asynchronously.
		m.reader.SetContent("")
		m.view = viewReader
		// Pass the item's provider so the body load can fall back to a network
		// fetch when the stored body is empty (Gmail). A nil entry (RSS, or an
		// unknown source) skips the fallback — RSS bodies are already cached — as
		// does an item already hydrated this session, so a repeat open never re-hits
		// the network.
		prov := m.providers[it.SourceID]
		if m.hydrated[it.ID] {
			prov = nil
		}
		cmds := []tea.Cmd{loadBodyCmd(m.store, m.renderer, prov, it)}
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

// statusBar renders the help line, prefixed with the sync indicator, the active
// filter hint, and the last status/error message when present. It uses the theme
// cached on the model rather than rebuilding it.
func (m Model) statusBar() string {
	helpLine := m.helpView
	left := m.status
	if sync := m.syncStatus(); sync != "" {
		if left != "" {
			left += "  "
		}
		left += sync
	}
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

// syncStatus renders the background-sync indicator: a spinner while the initial
// sweep is in flight, otherwise "synced <rel> ago · N sources" once a result has
// arrived. It is empty before the first result (and when there are no sources).
func (m Model) syncStatus() string {
	if m.sources == 0 {
		return ""
	}
	if m.syncing {
		return fmt.Sprintf("%s syncing… · %d sources", m.spinner.View(), m.sources)
	}
	if m.lastSync.IsZero() {
		return ""
	}
	rel := relTime(time.Since(m.lastSync))
	if rel == "just now" {
		return fmt.Sprintf("synced just now · %d sources", m.sources)
	}
	return fmt.Sprintf("synced %s ago · %d sources", rel, m.sources)
}

// relTime renders a short, human relative duration for the status bar: "just now"
// under five seconds, then seconds, minutes, or hours.
func relTime(d time.Duration) string {
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
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

// sourceLabel is the user-facing name for an item's source, used in the
// mark-source-read confirmation. It prefers the display name and falls back to the
// stable source id when the name is empty.
func sourceLabel(it model.Item) string {
	if it.SourceName != "" {
		return it.SourceName
	}
	return it.SourceID
}
