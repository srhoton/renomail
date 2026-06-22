package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/render"
	"github.com/srhoton/renomail/internal/source"
	"github.com/srhoton/renomail/internal/store"
	"github.com/srhoton/renomail/internal/syncengine"
)

// newSeededStore opens a fresh temp-dir store with no items, for tests that upsert
// their own fixtures.
func newSeededStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ui.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// mustRenderer builds the markdown renderer used by the body-load tests.
func mustRenderer(t *testing.T) *render.Renderer {
	t.Helper()
	r, err := render.New(defaultWidth)
	if err != nil {
		t.Fatalf("render.New() error = %v", err)
	}
	return r
}

// stubProvider is a source.Provider whose Body fills the item from memory, used to
// exercise the reader's network body-on-open fallback without a real network.
type stubProvider struct {
	id   string
	body string
}

func (s stubProvider) ID() string                                             { return s.id }
func (s stubProvider) Name() string                                           { return s.id }
func (s stubProvider) Kind() model.Kind                                       { return model.KindEmail }
func (s stubProvider) Fetch(context.Context, time.Time) ([]model.Item, error) { return nil, nil }
func (s stubProvider) Body(_ context.Context, it *model.Item) error {
	it.BodyText = s.body
	return nil
}

// seedItems are two items — one unread, one read — used across the UI tests.
func seedItems() []model.Item {
	now := time.Now()
	return []model.Item{
		{
			ID: "a", Kind: model.KindRSS, SourceName: "Feed A", Title: "Unread One",
			Published: now.Add(-1 * time.Hour), Read: false,
			BodyText: "body of unread one",
		},
		{
			ID: "b", Kind: model.KindRSS, SourceName: "Feed B", Title: "Read Two",
			Published: now.Add(-2 * time.Hour), Read: true,
			BodyText: "body of read two",
		},
	}
}

// newSeededModel opens a temp store, upserts the seed items, and returns a sized
// root model ready to drive.
func newSeededModel(t *testing.T) (Model, *store.Store) {
	t.Helper()
	st := newSeededStore(t)
	if _, err := st.UpsertItems(context.Background(), seedItems()); err != nil {
		t.Fatalf("UpsertItems() error = %v", err)
	}
	m, err := New(st, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return m, st
}

// step is a helper that applies a message and casts the result back to Model.
func step(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	nm, cmd := m.Update(msg)
	got, ok := nm.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want ui.Model", nm)
	}
	return got, cmd
}

func TestUpdate_windowSize_resizesWithoutPanic(t *testing.T) {
	m, _ := newSeededModel(t)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.w != 100 || m.h != 30 {
		t.Errorf("size = (%d,%d), want (100,30)", m.w, m.h)
	}
}

func TestUpdate_itemsLoaded_populatesFeed(t *testing.T) {
	m, _ := newSeededModel(t)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: seedItems()})

	it, ok := m.feed.Selected()
	if !ok {
		t.Fatal("feed has no selection after itemsLoadedMsg")
	}
	if it.Title != "Unread One" {
		t.Errorf("selected title = %q, want %q", it.Title, "Unread One")
	}
}

func TestUpdate_openSwitchesToReaderAndEmitsBodyLoad(t *testing.T) {
	m, _ := newSeededModel(t)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: seedItems()})

	m, cmd := step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.view != viewReader {
		t.Errorf("view = %d, want viewReader", m.view)
	}
	if cmd == nil {
		t.Fatal("Open returned nil cmd, want a body-load command")
	}
}

func TestUpdate_back_returnsToFeed(t *testing.T) {
	m, _ := newSeededModel(t)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: seedItems()})
	m, _ = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.view != viewReader {
		t.Fatalf("precondition: view = %d, want viewReader", m.view)
	}

	m, _ = step(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.view != viewFeed {
		t.Errorf("view = %d, want viewFeed after back", m.view)
	}
}

func TestUpdate_help_toggles(t *testing.T) {
	m, _ := newSeededModel(t)
	if m.help.ShowAll {
		t.Fatal("help starts expanded, want collapsed")
	}
	m, _ = step(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
	if !m.help.ShowAll {
		t.Error("help did not expand on '?'")
	}
	m, _ = step(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
	if m.help.ShowAll {
		t.Error("help did not collapse on second '?'")
	}
}

func TestUpdate_quit_emitsQuit(t *testing.T) {
	m, _ := newSeededModel(t)
	_, cmd := step(t, m, tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("quit returned nil cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("quit cmd did not produce tea.QuitMsg")
	}
}

func TestUpdate_bodyLoaded_setsReaderContent(t *testing.T) {
	m, _ := newSeededModel(t)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: seedItems()})
	m, _ = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // opens "a", sets openID
	m, _ = step(t, m, bodyLoadedMsg{id: "a", rendered: "RENDERED-BODY-MARKER"})
	if got := m.reader.View(); !strings.Contains(got, "RENDERED-BODY-MARKER") {
		t.Errorf("reader view missing rendered body:\n%s", got)
	}
}

func TestUpdate_bodyLoaded_staleIDIgnored(t *testing.T) {
	m, _ := newSeededModel(t)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: seedItems()})
	m, _ = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // opens "a"

	// A body for a different (previously-open) item must not land in the reader.
	m, _ = step(t, m, bodyLoadedMsg{id: "stale-other-id", rendered: "STALE-MARKER"})
	if got := m.reader.View(); strings.Contains(got, "STALE-MARKER") {
		t.Errorf("stale body leaked into reader:\n%s", got)
	}
}

func TestUpdate_bodyLoadedError_setsStatus(t *testing.T) {
	m, _ := newSeededModel(t)
	m.openID = "a" // simulate "a" being the open item
	m, _ = step(t, m, bodyLoadedMsg{id: "a", err: errors.New("render boom")})
	if !strings.Contains(m.status, "render boom") {
		t.Errorf("status = %q, want it to contain %q", m.status, "render boom")
	}
}

func TestUpdate_errMsg_setsStatus(t *testing.T) {
	m, _ := newSeededModel(t)
	m, _ = step(t, m, errMsg{err: errors.New("query boom")})
	if !strings.Contains(m.status, "query boom") {
		t.Errorf("status = %q, want it to contain %q", m.status, "query boom")
	}
}

func TestView_rendersFeedThenReader(t *testing.T) {
	m, _ := newSeededModel(t)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: seedItems()})

	if v := m.View(); !strings.Contains(v.Content, "Unread One") {
		t.Errorf("feed view missing item title:\n%s", v.Content)
	}
	if !m.View().AltScreen {
		t.Error("View().AltScreen = false, want true")
	}

	m, _ = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // opens "a", sets openID + view
	m, _ = step(t, m, bodyLoadedMsg{id: "a", rendered: "READER-MARKER"})
	if v := m.View(); !strings.Contains(v.Content, "READER-MARKER") {
		t.Errorf("reader view missing body:\n%s", v.Content)
	}
}

func TestLoadBodyCmd_hydratesAndRenders(t *testing.T) {
	m, st := newSeededModel(t)

	msg := loadBodyCmd(st, m.renderer, nil, model.Item{ID: "a"})()
	bl, ok := msg.(bodyLoadedMsg)
	if !ok {
		t.Fatalf("loadBodyCmd produced %T, want bodyLoadedMsg", msg)
	}
	if bl.err != nil {
		t.Fatalf("loadBodyCmd err = %v", bl.err)
	}
	if !strings.Contains(bl.rendered, "unread") {
		t.Errorf("rendered body missing source text: %q", bl.rendered)
	}
}

func TestLoadBodyCmd_missingItem_returnsError(t *testing.T) {
	m, st := newSeededModel(t)

	msg := loadBodyCmd(st, m.renderer, nil, model.Item{ID: "does-not-exist"})()
	bl, ok := msg.(bodyLoadedMsg)
	if !ok {
		t.Fatalf("loadBodyCmd produced %T, want bodyLoadedMsg", msg)
	}
	if bl.err == nil {
		t.Error("loadBodyCmd err = nil, want error for missing item")
	}
}

// TestLoadBodyCmd_emptyStoredBody_fallsBackToProvider covers the Gmail body-on-open
// path: an item stored without a body triggers the provider's network Body load,
// the rendered output reflects it, and the body is cached in the store for next time.
func TestLoadBodyCmd_emptyStoredBody_fallsBackToProvider(t *testing.T) {
	ctx := context.Background()
	st := newSeededStore(t)
	// A body-less item, as Gmail's Fetch stores it.
	bodyless := model.Item{
		ID: "mail1", Kind: model.KindEmail, SourceID: "gmail:me", SourceName: "me",
		Title: "Mail", NativeID: "n1", Published: time.Now(),
	}
	if _, err := st.UpsertItems(ctx, []model.Item{bodyless}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	r := mustRenderer(t)
	// A single token, so the renderer's per-word ANSI styling cannot split it.
	const marker = "fetchedbodymarker"
	prov := stubProvider{id: "gmail:me", body: marker}

	msg := loadBodyCmd(st, r, prov, bodyless)()
	bl, ok := msg.(bodyLoadedMsg)
	if !ok || bl.err != nil {
		t.Fatalf("loadBodyCmd msg = %#v (ok=%v), want a clean bodyLoadedMsg", msg, ok)
	}
	if !strings.Contains(bl.rendered, marker) {
		t.Errorf("rendered body missing provider text: %q", bl.rendered)
	}
	// The body must now be cached, so a subsequent store read returns it.
	_, text, err := st.GetBody(ctx, "mail1")
	if err != nil {
		t.Fatalf("GetBody: %v", err)
	}
	if !strings.Contains(text, marker) {
		t.Errorf("body not cached after fallback: %q", text)
	}
}

func TestUpdate_syncBatch_withItems_requeriesAndKeepsRunning(t *testing.T) {
	m, _ := newSeededModel(t)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	res := syncengine.Result{SourceID: "a", SourceName: "Feed A", Items: seedItems()}
	m, cmd := step(t, m, syncBatchMsg{res})

	if cmd == nil {
		t.Fatal("syncBatchMsg returned nil cmd, want re-query + re-armed listener")
	}
	if m.lastSync.IsZero() {
		t.Error("lastSync not recorded after a sync batch")
	}
	if m.status != "" {
		t.Errorf("status = %q, want empty on a successful batch", m.status)
	}
}

func TestUpdate_syncBatch_withError_setsStatusAndDoesNotPanic(t *testing.T) {
	m, _ := newSeededModel(t)

	res := syncengine.Result{SourceID: "b", SourceName: "Feed B", Err: errors.New("boom")}
	m, cmd := step(t, m, syncBatchMsg{res})

	if cmd == nil {
		t.Fatal("syncBatchMsg(err) returned nil cmd, want a re-armed listener")
	}
	if !strings.Contains(m.status, "Feed B") || !strings.Contains(m.status, "boom") {
		t.Errorf("status = %q, want it to surface the source name and error", m.status)
	}
}

func TestUpdate_syncBatch_initialSweepStopsSpinner(t *testing.T) {
	st := newSeededStore(t)
	prov := stubProvider{id: "gmail:me", body: "x"}
	m, err := New(st, make(chan syncengine.Result), []source.Provider{prov}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !m.syncing || m.inflight != 1 {
		t.Fatalf("initial state syncing=%v inflight=%d, want true/1", m.syncing, m.inflight)
	}

	res := syncengine.Result{SourceID: "gmail:me", SourceName: "me"}
	m, _ = step(t, m, syncBatchMsg{res})

	if m.syncing || m.inflight != 0 {
		t.Errorf("after the only provider reported: syncing=%v inflight=%d, want false/0", m.syncing, m.inflight)
	}
}

// steadyStateModel returns a seeded model whose events channel is already closed,
// so the re-armed waitForActivity listener returns immediately (nil) rather than
// blocking when a test executes the commands batched by handleSyncBatch. With no
// providers, inflight is 0, so a sync batch is treated as steady-state.
func steadyStateModel(t *testing.T) Model {
	t.Helper()
	st := newSeededStore(t)
	if _, err := st.UpsertItems(context.Background(), seedItems()); err != nil {
		t.Fatalf("UpsertItems() error = %v", err)
	}
	ch := make(chan syncengine.Result)
	close(ch)
	m, err := New(st, ch, nil, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return m
}

func TestUpdate_syncBatch_steadyState_notifiesPerSource(t *testing.T) {
	m := steadyStateModel(t)
	var got string
	m.notify = func(msg string) error { got = msg; return nil }

	res := syncengine.Result{SourceID: "a", SourceName: "Feed A", Items: seedItems(), Inserted: 3}
	_, cmd := step(t, m, syncBatchMsg{res})
	if cmd == nil {
		t.Fatal("syncBatchMsg returned nil cmd")
	}
	// Run the batched commands to fire the notifier; the re-query and the (closed)
	// listener are harmless to execute here.
	runCmdMsgs(t, cmd())

	if want := "renomail: 3 new from Feed A"; got != want {
		t.Errorf("notification = %q, want %q", got, want)
	}
}

func TestUpdate_syncBatch_initialSweep_doesNotNotify(t *testing.T) {
	st := newSeededStore(t)
	prov := stubProvider{id: "gmail:me", body: "x"}
	ch := make(chan syncengine.Result)
	close(ch) // re-armed listener returns immediately instead of blocking
	m, err := New(st, ch, []source.Provider{prov}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Precondition: the initial sweep is in flight (inflight == 1).
	if !m.syncing || m.inflight != 1 {
		t.Fatalf("precondition syncing=%v inflight=%d, want true/1", m.syncing, m.inflight)
	}
	called := false
	m.notify = func(string) error { called = true; return nil }

	// Even with new items, an initial-sweep batch must not notify.
	res := syncengine.Result{SourceID: "gmail:me", SourceName: "me", Inserted: 5}
	_, cmd := step(t, m, syncBatchMsg{res})
	runCmdMsgs(t, cmd())

	if called {
		t.Error("notifier fired during the initial sweep, want it suppressed")
	}
}

func TestUpdate_syncBatch_noNewItems_doesNotNotify(t *testing.T) {
	m := steadyStateModel(t)
	called := false
	m.notify = func(string) error { called = true; return nil }

	// Items re-seen but nothing inserted (Inserted == 0): no notification.
	res := syncengine.Result{SourceID: "a", SourceName: "Feed A", Items: seedItems(), Inserted: 0}
	_, cmd := step(t, m, syncBatchMsg{res})
	runCmdMsgs(t, cmd())

	if called {
		t.Error("notifier fired with zero inserted items, want it suppressed")
	}
}

func TestUpdate_syncBatch_notifyError_surfacesOnStatus(t *testing.T) {
	m := steadyStateModel(t)
	m.notify = func(string) error { return errors.New("no tmux server") }

	res := syncengine.Result{SourceID: "a", SourceName: "Feed A", Inserted: 1}
	_, cmd := step(t, m, syncBatchMsg{res})

	// The notifier error rides back as an errMsg; feeding it to the model surfaces
	// it on the status line (mirrors the open-in-browser error path).
	if msg := findErrMsg(cmd); msg != nil {
		nm, _ := step(t, m, *msg)
		if !strings.Contains(nm.status, "no tmux server") {
			t.Errorf("status = %q, want the notifier error surfaced", nm.status)
		}
	} else {
		t.Fatal("no errMsg produced by the failing notifier")
	}
}

func TestNew_defaultNotifierIsNoop(t *testing.T) {
	m, _ := newSeededModel(t)
	if m.notify == nil {
		t.Fatal("default notify is nil, want a no-op function")
	}
	if err := m.notify("anything"); err != nil {
		t.Errorf("default notify returned %v, want nil (no-op)", err)
	}
}

// runCmdMsgs recursively executes a (possibly batched) command's messages so any
// injected side effects (the notifier, store re-query) run. Non-batch messages are
// discarded. Tests using it must supply a model whose events channel is closed, so
// the re-armed waitForActivity listener returns immediately rather than blocking.
func runCmdMsgs(t *testing.T, msg tea.Msg) {
	t.Helper()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return
	}
	for _, c := range batch {
		if c != nil {
			runCmdMsgs(t, c())
		}
	}
}

// findErrMsg walks a batched command tree and returns the first errMsg produced,
// or nil if none. Used to assert the notifier's failure path.
func findErrMsg(cmd tea.Cmd) *errMsg {
	var out *errMsg
	var walk func(tea.Msg)
	walk = func(msg tea.Msg) {
		switch m := msg.(type) {
		case errMsg:
			if out == nil {
				em := m
				out = &em
			}
		case tea.BatchMsg:
			for _, c := range m {
				if c != nil {
					walk(c())
				}
			}
		}
	}
	if cmd != nil {
		walk(cmd())
	}
	return out
}

func TestUpdate_openInBrowser_feedAndReader(t *testing.T) {
	m, _ := newSeededModel(t)
	var got string
	m.openURL = func(u string) error { got = u; return nil }
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	items := []model.Item{{
		ID: "a", Kind: model.KindRSS, SourceName: "Feed A", Title: "One",
		URL: "https://example.com/a", BodyText: "body",
	}}
	m, _ = step(t, m, itemsLoadedMsg{items: items})

	// Feed view: 'o' opens the selected row's URL.
	_, cmd := step(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"})
	if cmd == nil {
		t.Fatal("'o' returned nil cmd in feed view")
	}
	if msg := cmd(); msg != nil {
		t.Errorf("open command produced %#v, want nil on success", msg)
	}
	if got != "https://example.com/a" {
		t.Errorf("opened URL = %q, want the selected item's URL", got)
	}

	// Reader view: open the item, then 'o' opens the open item's URL.
	got = ""
	m, _ = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.view != viewReader {
		t.Fatalf("precondition: view = %d, want viewReader", m.view)
	}
	_, cmd = step(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"})
	if cmd == nil {
		t.Fatal("'o' returned nil cmd in reader view")
	}
	_ = cmd()
	if got != "https://example.com/a" {
		t.Errorf("reader 'o' opened %q, want the open item's URL", got)
	}
}

func TestUpdate_openInBrowser_noURLIsNoop(t *testing.T) {
	m, _ := newSeededModel(t)
	called := false
	m.openURL = func(string) error { called = true; return nil }
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: seedItems()}) // seed items have no URL

	_, cmd := step(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"})
	if cmd != nil {
		t.Error("'o' on a URL-less item returned a command, want nil (no-op)")
	}
	if called {
		t.Error("opener was invoked for a URL-less item")
	}
}

func TestUpdate_openInBrowser_errorSurfacesOnStatus(t *testing.T) {
	m, _ := newSeededModel(t)
	m.openURL = func(string) error { return errors.New("no browser") }
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: []model.Item{{
		ID: "a", Kind: model.KindRSS, Title: "One", URL: "https://example.com/a",
	}}})

	_, cmd := step(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"})
	msg := cmd()
	if em, ok := msg.(errMsg); !ok || em.err == nil {
		t.Fatalf("open failure produced %#v, want an errMsg", msg)
	}
}

func TestUpdate_forceSync_triggersAndRelightsSpinner(t *testing.T) {
	st := newSeededStore(t)
	prov := stubProvider{id: "gmail:me", body: "x"}
	calls := 0
	m, err := New(st, make(chan syncengine.Result), []source.Provider{prov}, func() { calls++ }, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Drive the initial sweep to completion so the model is idle before forcing.
	m, _ = step(t, m, syncBatchMsg{syncengine.Result{SourceID: "gmail:me", SourceName: "me"}})
	if m.syncing {
		t.Fatal("precondition: still syncing after the only provider reported")
	}

	m, cmd := step(t, m, tea.KeyPressMsg{Code: 'R', Text: "R"})
	if calls != 1 {
		t.Errorf("triggerSync called %d times, want 1", calls)
	}
	if !m.syncing || m.inflight != 1 {
		t.Errorf("after 'R': syncing=%v inflight=%d, want true/1", m.syncing, m.inflight)
	}
	if cmd == nil {
		t.Error("'R' returned nil cmd, want a spinner tick")
	}

	// A second 'R' while a sweep is in flight must be a no-op (no double count).
	_, _ = step(t, m, tea.KeyPressMsg{Code: 'R', Text: "R"})
	if calls != 1 {
		t.Errorf("'R' while syncing triggered again (calls=%d), want it to be a no-op", calls)
	}
}

func TestUpdate_resizeInReader_reRendersBody(t *testing.T) {
	m, _ := newSeededModel(t)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: seedItems()})
	m, _ = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open "a" → reader
	if m.view != viewReader || m.openID != "a" {
		t.Fatalf("precondition: view=%d openID=%q, want reader/a", m.view, m.openID)
	}
	// Let the open's body render settle first: renders are single-flight, so a resize
	// only kicks off the re-wrap once the in-flight render has completed (the realistic
	// runtime order — renders finish in milliseconds, well before a human resizes).
	m, _ = step(t, m, bodyLoadedMsg{id: "a", rendered: "BODY-A"})

	_, cmd := step(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	if cmd == nil {
		t.Fatal("resize in reader returned nil cmd, want a re-render body load")
	}
	bl, ok := cmd().(bodyLoadedMsg)
	if !ok {
		t.Fatalf("resize cmd produced %T, want bodyLoadedMsg", cmd())
	}
	if bl.err != nil {
		t.Fatalf("resize re-render err = %v", bl.err)
	}
	if bl.id != "a" {
		t.Errorf("re-rendered id = %q, want the open item a", bl.id)
	}
}

func TestUpdate_keyClearsTransientStatus(t *testing.T) {
	m, _ := newSeededModel(t)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: seedItems()})
	m.status = "stale sync error"

	m, _ = step(t, m, tea.KeyPressMsg{Code: 'j', Text: "j"}) // any non-filter key
	if m.status != "" {
		t.Errorf("status = %q, want it cleared on the next keypress", m.status)
	}
}

func TestHandleFeedKey_markSourceRead_marksOnlySelectedSource(t *testing.T) {
	st := newSeededStore(t)
	now := time.Now()
	items := []model.Item{
		{ID: "a1", Kind: model.KindRSS, SourceID: "feed:a", SourceName: "Feed A", Title: "A One", Published: now.Add(-1 * time.Minute)},
		{ID: "a2", Kind: model.KindRSS, SourceID: "feed:a", SourceName: "Feed A", Title: "A Two", Published: now.Add(-2 * time.Minute)},
		{ID: "b1", Kind: model.KindRSS, SourceID: "feed:b", SourceName: "Feed B", Title: "B One", Published: now.Add(-3 * time.Minute)},
	}
	if _, err := st.UpsertItems(context.Background(), items); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	m, err := New(st, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: items})

	// The newest item (a1, Feed A) is selected first.
	if sel, ok := m.feed.Selected(); !ok || sel.SourceID != "feed:a" {
		t.Fatalf("precondition: selected = %#v (ok=%v), want a Feed A row", sel, ok)
	}

	m, cmd := step(t, m, tea.KeyPressMsg{Code: 'S', Text: "S"})
	if cmd == nil {
		t.Fatal("'S' returned nil cmd, want a mark+reload command")
	}
	if _, ok := cmd().(reloadMsg); !ok {
		t.Fatal("'S' cmd did not produce reloadMsg")
	}
	if !strings.Contains(m.status, "Feed A") {
		t.Errorf("status = %q, want it to name the marked source", m.status)
	}

	// The whole of Feed A is now read; Feed B is untouched.
	read, err := st.Query(context.Background(), model.Filter{Read: model.ReadReadOnly})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	readIDs := map[string]bool{}
	for _, it := range read {
		readIDs[it.ID] = true
	}
	if !readIDs["a1"] || !readIDs["a2"] {
		t.Errorf("Feed A not fully read; read set = %v, want a1 and a2", readIDs)
	}
	if readIDs["b1"] {
		t.Error("Feed B item was marked read, want it left untouched")
	}
}

func TestHandleFeedKey_markSourceRead_emptyFeedIsNoop(t *testing.T) {
	st := newSeededStore(t) // no items
	m, err := New(st, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: nil})

	if _, cmd := step(t, m, tea.KeyPressMsg{Code: 'S', Text: "S"}); cmd != nil {
		t.Error("'S' on an empty feed returned a command, want nil (no-op)")
	}
}

func TestHandleFeedKey_markSourceRead_emptySourceIDIsNoop(t *testing.T) {
	m, _ := newSeededModel(t)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	// A selected row with no SourceID must not trigger a mark — an empty id would
	// otherwise be passed straight through to the source filter.
	m, _ = step(t, m, itemsLoadedMsg{items: []model.Item{
		{ID: "x", Kind: model.KindRSS, SourceName: "Orphan", Title: "No Source"},
	}})

	if _, cmd := step(t, m, tea.KeyPressMsg{Code: 'S', Text: "S"}); cmd != nil {
		t.Error("'S' on an item with no SourceID returned a command, want nil (no-op)")
	}
}

func TestSourceLabel(t *testing.T) {
	tests := []struct {
		name string
		it   model.Item
		want string
	}{
		{"prefers display name", model.Item{SourceName: "Feed A", SourceID: "feed:a"}, "Feed A"},
		{"falls back to id when name is empty", model.Item{SourceID: "feed:a"}, "feed:a"},
	}
	for _, tt := range tests {
		if got := sourceLabel(tt.it); got != tt.want {
			t.Errorf("%s: sourceLabel = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestRelTime(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{2 * time.Second, "just now"},
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
	}
	for _, tt := range tests {
		if got := relTime(tt.d); got != tt.want {
			t.Errorf("relTime(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestSetReadCmd_persistsReadFlag(t *testing.T) {
	_, st := newSeededModel(t)

	msg, ok := setReadCmd(st, "a", true)().(readToggledMsg)
	if !ok {
		t.Fatalf("setReadCmd produced %T, want readToggledMsg", setReadCmd(st, "a", true)())
	}
	if msg.id != "a" || !msg.read || msg.err != nil {
		t.Errorf("setReadCmd msg = %+v, want {id:a read:true err:nil}", msg)
	}
	items, err := st.Query(context.Background(), model.Filter{})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	for _, it := range items {
		if it.ID == "a" && !it.Read {
			t.Error("item a not marked read after setReadCmd")
		}
	}
}

func TestStatusBar_withStatus_includesMessage(t *testing.T) {
	m, _ := newSeededModel(t)
	m.status = "status boom"
	if got := m.statusBar(); !strings.Contains(got, "status boom") {
		t.Errorf("statusBar() = %q, want it to contain the status message", got)
	}
}

// loadedModel returns a seeded, sized model with the feed already populated —
// the common precondition for the filter and read-state key tests.
func loadedModel(t *testing.T) (Model, *store.Store) {
	t.Helper()
	m, st := newSeededModel(t)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: seedItems()})
	return m, st
}

// keyPress is a small helper for letter/rune key presses.
func keyPress(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func TestHandleFeedKey_filterEmail_setsKindAndReQueries(t *testing.T) {
	m, _ := loadedModel(t)
	m, cmd := step(t, m, keyPress('e'))
	if !m.filter.Kinds[model.KindEmail] {
		t.Error("e did not set filter.Kinds[email]")
	}
	if cmd == nil {
		t.Fatal("e returned nil cmd, want a re-query")
	}
	if _, ok := cmd().(itemsLoadedMsg); !ok {
		t.Error("e re-query did not produce itemsLoadedMsg")
	}
}

func TestHandleFeedKey_filterRSS_setsKind(t *testing.T) {
	m, _ := loadedModel(t)
	m, cmd := step(t, m, keyPress('r'))
	if !m.filter.Kinds[model.KindRSS] {
		t.Error("r did not set filter.Kinds[rss]")
	}
	if cmd == nil {
		t.Error("r returned nil cmd, want a re-query")
	}
}

func TestHandleFeedKey_cycleRead_cyclesReadState(t *testing.T) {
	m, _ := loadedModel(t)
	// Starting from ReadAny, u cycles all -> unread-only -> read-only -> all,
	// re-querying on every press.
	want := []model.ReadState{model.ReadUnreadOnly, model.ReadReadOnly, model.ReadAny}
	for i, w := range want {
		var cmd tea.Cmd
		m, cmd = step(t, m, keyPress('u'))
		if m.filter.Read != w {
			t.Errorf("press %d: Read = %v, want %v", i+1, m.filter.Read, w)
		}
		if cmd == nil {
			t.Errorf("press %d: u returned nil cmd, want a re-query", i+1)
		}
	}
}

func TestHandleFeedKey_filterAll_resetsFilter(t *testing.T) {
	m, _ := loadedModel(t)
	m, _ = step(t, m, keyPress('r'))    // scope to rss
	m, _ = step(t, m, keyPress('u'))    // unread only
	m, cmd := step(t, m, keyPress('a')) // reset
	if len(m.filter.Kinds) != 0 || m.filter.Read != model.ReadAny || m.filter.Search != "" {
		t.Errorf("a did not reset the filter: %+v", m.filter)
	}
	if cmd == nil {
		t.Error("a returned nil cmd, want a re-query")
	}
}

func TestHandleFeedKey_search_entersFilterViewFocused(t *testing.T) {
	m, _ := loadedModel(t)
	m.filter.Search = "preset"
	m, cmd := step(t, m, keyPress('/'))
	if m.view != viewFilter {
		t.Errorf("view = %d after '/', want viewFilter", m.view)
	}
	if !m.filterbar.Focused() {
		t.Error("filter bar not focused after '/'")
	}
	if m.filterbar.Value() != "preset" {
		t.Errorf("filter bar value = %q, want the active term pre-filled", m.filterbar.Value())
	}
	if cmd == nil {
		t.Error("'/' returned nil cmd, want the focus blink command")
	}
}

func TestFilterView_typeAndApply_setsSearchAndReQueries(t *testing.T) {
	m, _ := loadedModel(t)
	m, _ = step(t, m, keyPress('/'))
	for _, r := range "k8s" {
		m, _ = step(t, m, keyPress(r))
	}
	if m.filterbar.Value() != "k8s" {
		t.Fatalf("filter bar value = %q, want typed term reaching it", m.filterbar.Value())
	}
	m, cmd := step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.view != viewFeed {
		t.Errorf("view = %d after Enter, want viewFeed", m.view)
	}
	if m.filter.Search != "k8s" {
		t.Errorf("filter.Search = %q after Enter, want %q", m.filter.Search, "k8s")
	}
	if cmd == nil || func() bool { _, ok := cmd().(itemsLoadedMsg); return !ok }() {
		t.Error("Enter did not re-query with itemsLoadedMsg")
	}
}

func TestFilterView_escCancels_keepsExistingFilter(t *testing.T) {
	m, _ := loadedModel(t)
	m.filter.Search = "keepme"
	m, _ = step(t, m, keyPress('/'))
	for _, r := range "junk" {
		m, _ = step(t, m, keyPress(r))
	}
	m, _ = step(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.view != viewFeed {
		t.Errorf("view = %d after Esc, want viewFeed", m.view)
	}
	if m.filter.Search != "keepme" {
		t.Errorf("filter.Search = %q after Esc, want it unchanged (%q)", m.filter.Search, "keepme")
	}
}

func TestFilterView_typingQ_doesNotQuit(t *testing.T) {
	m, _ := loadedModel(t)
	m, _ = step(t, m, keyPress('/'))
	m, cmd := step(t, m, keyPress('q'))
	if m.view != viewFilter {
		t.Errorf("view = %d after typing q in filter, want viewFilter (no quit)", m.view)
	}
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Error("typing q in the search box quit the app")
		}
	}
	if m.filterbar.Value() != "q" {
		t.Errorf("filter bar value = %q, want the typed q", m.filterbar.Value())
	}
}

func TestFilterView_ctrlC_quits(t *testing.T) {
	m, _ := loadedModel(t)
	m, _ = step(t, m, keyPress('/'))
	_, cmd := step(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c in filter returned nil cmd, want quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("ctrl+c in filter did not quit")
	}
}

func TestHandleFeedKey_toggleRead_flipsAndPersists(t *testing.T) {
	m, _ := loadedModel(t) // "a" (unread) is selected
	m, cmd := step(t, m, keyPress('m'))
	it, ok := m.feed.Selected()
	if !ok || !it.Read {
		t.Fatalf("selected read = %v after m, want optimistic true", it.Read)
	}
	if cmd == nil {
		t.Fatal("m returned nil cmd, want setReadCmd")
	}
	msg, ok := cmd().(readToggledMsg)
	if !ok {
		t.Fatalf("m cmd produced %T, want readToggledMsg", cmd())
	}
	if msg.id != "a" || !msg.read || msg.err != nil {
		t.Errorf("readToggledMsg = %+v, want {id:a read:true err:nil}", msg)
	}
}

// TestUpdate_readToggled_leavesActiveFilter covers both directions in which a read
// toggle pushes an item out of the active read filter: a now-read item under the
// unread-only filter, and a now-unread item under the read-only filter. In each
// case the affected row is dropped from the feed in place (no re-query / nil cmd),
// and only that row — the sibling stays.
func TestUpdate_readToggled_leavesActiveFilter(t *testing.T) {
	tests := []struct {
		name      string
		filter    model.ReadState
		id        string // item whose read flag was toggled
		read      bool   // its new read state
		gone      string // title that must disappear from the feed
		remaining string // title that must remain
	}{
		{
			name:   "unread-only filter drops a now-read item",
			filter: model.ReadUnreadOnly,
			id:     "a", read: true,
			gone: "Unread One", remaining: "Read Two",
		},
		{
			name:   "read-only filter drops a now-unread item",
			filter: model.ReadReadOnly,
			id:     "b", read: false,
			gone: "Read Two", remaining: "Unread One",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := loadedModel(t)
			m.filter.Read = tt.filter
			m, cmd := step(t, m, readToggledMsg{id: tt.id, read: tt.read})
			if cmd != nil {
				t.Error("readToggledMsg leaving the filter returned a non-nil cmd, want nil (row dropped in place)")
			}
			view := m.feed.View()
			if strings.Contains(view, tt.gone) {
				t.Errorf("feed still shows %q after it left the filter:\n%s", tt.gone, view)
			}
			if !strings.Contains(view, tt.remaining) {
				t.Errorf("feed dropped the wrong row; %q is missing:\n%s", tt.remaining, view)
			}
		})
	}
}

func TestUpdate_readToggled_error_setsStatus(t *testing.T) {
	m, _ := loadedModel(t)
	m, _ = step(t, m, readToggledMsg{id: "a", err: errors.New("persist boom")})
	if !strings.Contains(m.status, "persist boom") {
		t.Errorf("status = %q, want the persistence error", m.status)
	}
}

func TestHandleFeedKey_markAllRead_persistsAndReloads(t *testing.T) {
	m, st := loadedModel(t)
	_, cmd := step(t, m, keyPress('M'))
	if cmd == nil {
		t.Fatal("M returned nil cmd, want markAllReadCmd")
	}
	if _, ok := cmd().(reloadMsg); !ok {
		t.Fatalf("M cmd produced %T, want reloadMsg", cmd())
	}
	// The store write happened when the command ran.
	items, err := st.Query(context.Background(), model.Filter{})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	for _, it := range items {
		if !it.Read {
			t.Errorf("item %s still unread after mark-all-read", it.ID)
		}
	}
}

func TestUpdate_reloadMsg_reQueries(t *testing.T) {
	m, _ := loadedModel(t)
	_, cmd := step(t, m, reloadMsg{})
	if cmd == nil {
		t.Fatal("reloadMsg returned nil cmd, want a re-query")
	}
	if _, ok := cmd().(itemsLoadedMsg); !ok {
		t.Error("reloadMsg did not re-query with itemsLoadedMsg")
	}
}

func TestStatusBar_showsFilterHint(t *testing.T) {
	m, _ := newSeededModel(t)
	m.filter = model.Filter{
		Kinds:  map[model.Kind]bool{model.KindRSS: true},
		Read:   model.ReadUnreadOnly,
		Search: "k8s",
	}
	m.applyFilter() // refresh the cached hint the status bar reads
	got := m.statusBar()
	for _, want := range []string{"Filter:", "rss", "unread", "k8s"} {
		if !strings.Contains(got, want) {
			t.Errorf("statusBar() = %q, want it to contain %q", got, want)
		}
	}
}

func TestFilterHint(t *testing.T) {
	tests := []struct {
		name   string
		filter model.Filter
		want   string
	}{
		{"zero", model.Filter{}, ""},
		{"email", model.Filter{Kinds: map[model.Kind]bool{model.KindEmail: true}}, "email"},
		{"rss", model.Filter{Kinds: map[model.Kind]bool{model.KindRSS: true}}, "rss"},
		{"both kinds", model.Filter{Kinds: map[model.Kind]bool{model.KindEmail: true, model.KindRSS: true}}, "email · rss"},
		{"unread", model.Filter{Read: model.ReadUnreadOnly}, "unread"},
		{"read", model.Filter{Read: model.ReadReadOnly}, "read"},
		{"search", model.Filter{Search: "k8s"}, `"k8s"`},
		{"combined", model.Filter{Kinds: map[model.Kind]bool{model.KindRSS: true}, Read: model.ReadReadOnly, Search: "go"}, `rss · read · "go"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filterHint(tt.filter); got != tt.want {
				t.Errorf("filterHint(%+v) = %q, want %q", tt.filter, got, tt.want)
			}
		})
	}
}

func TestView_filterView_showsBar(t *testing.T) {
	m, _ := loadedModel(t)
	m, _ = step(t, m, keyPress('/'))
	for _, r := range "abc" {
		m, _ = step(t, m, keyPress(r))
	}
	if v := m.View(); !strings.Contains(v.Content, "abc") {
		t.Errorf("filter view missing the typed term:\n%s", v.Content)
	}
}

func TestReadState_survivesReupsert(t *testing.T) {
	m, st := loadedModel(t)
	// Mark everything read via the UI, which persists through markAllReadCmd.
	_, cmd := step(t, m, keyPress('M'))
	if _, ok := cmd().(reloadMsg); !ok {
		t.Fatal("M did not persist (no reloadMsg)")
	}
	// A subsequent re-sync (UpsertItems with the original unread flags) must not
	// clobber the local read state — the step-02 guard, re-asserted at the UI level.
	if _, err := st.UpsertItems(context.Background(), seedItems()); err != nil {
		t.Fatalf("UpsertItems() error = %v", err)
	}
	items, err := st.Query(context.Background(), model.Filter{})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	for _, it := range items {
		if !it.Read {
			t.Errorf("item %s reverted to unread after re-upsert", it.ID)
		}
	}
}

// TestTeatest_feedShowsTitlesAndDots drives the real program: Init loads items
// from the seeded store, and the feed must render both titles with the unread and
// read dot glyphs.
func TestTeatest_feedShowsTitlesAndDots(t *testing.T) {
	m, _ := newSeededModel(t)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 40))

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return strings.Contains(s, "Unread One") &&
			strings.Contains(s, "Read Two") &&
			strings.Contains(s, "●") && // unread dot
			strings.Contains(s, "○") // read dot
	}, teatest.WithDuration(3*time.Second))

	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// twoUnreadModel returns a sized, loaded model whose feed holds two unread items
// (selection on the first), with its events channel closed so runCmdMsgs never blocks
// on the re-armed listener. Used to exercise the preview pane's follow + mark-read.
func twoUnreadModel(t *testing.T) (Model, *store.Store) {
	t.Helper()
	st := newSeededStore(t)
	items := []model.Item{
		{
			ID: "a", Kind: model.KindRSS, SourceName: "Feed A", Title: "Unread One",
			Published: time.Now().Add(-1 * time.Hour), Read: false, BodyText: "body a",
		},
		{
			// Older than "a", so it sorts to the second row: opening the pane lands on
			// "a" and a single `j` moves the preview to "c".
			ID: "c", Kind: model.KindRSS, SourceName: "Feed C", Title: "Unread Three",
			Published: time.Now().Add(-90 * time.Minute), Read: false, BodyText: "body c",
		},
	}
	if _, err := st.UpsertItems(context.Background(), items); err != nil {
		t.Fatalf("UpsertItems() error = %v", err)
	}
	ch := make(chan syncengine.Result)
	close(ch)
	m, err := New(st, ch, nil, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: items})
	return m, st
}

func TestTogglePane_opensOnSelection(t *testing.T) {
	m, st := loadedModel(t)
	m, cmd := step(t, m, keyPress('p'))
	if !m.split {
		t.Fatal("split not set after p; pane did not open")
	}
	if m.openID != "a" {
		t.Errorf("openID = %q, want the selected item %q", m.openID, "a")
	}
	if cmd == nil {
		t.Fatal("opening the pane returned nil cmd, want a body-load (+ mark-read) command")
	}
	// Run the returned command's effects and confirm the selected item was actually
	// marked read in the store — a non-nil-but-wrong cmd would otherwise pass.
	runCmdMsgs(t, cmd())
	items, err := st.Query(context.Background(), model.Filter{})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	for _, it := range items {
		if it.ID == "a" && !it.Read {
			t.Error("item a not marked read after opening it in the pane")
		}
	}
}

func TestTogglePane_secondPressCloses(t *testing.T) {
	m, _ := loadedModel(t)
	m, _ = step(t, m, keyPress('p'))
	m, _ = step(t, m, keyPress('p'))
	if m.split {
		t.Error("split still set after a second p; pane did not close")
	}
	// With the pane closed the view stacks only feed + status.
	if v := m.View().Content; !strings.Contains(v, "Unread One") {
		t.Errorf("feed missing after closing pane:\n%s", v)
	}
}

func TestTogglePane_refusedWhenTerminalTooShort(t *testing.T) {
	m, _ := newSeededModel(t)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 6}) // childH = 5 < minSplitHeight
	m, _ = step(t, m, itemsLoadedMsg{items: seedItems()})

	m, _ = step(t, m, keyPress('p'))
	if m.split {
		t.Error("split set on a too-short terminal, want the toggle refused")
	}
	if !strings.Contains(m.status, "too short") {
		t.Errorf("status = %q, want a too-short hint", m.status)
	}
}

func TestTogglePane_followsSelectionAndMarksRead(t *testing.T) {
	m, st := twoUnreadModel(t)
	m, _ = step(t, m, keyPress('p')) // open the pane on "a"
	if m.openID != "a" {
		t.Fatalf("precondition openID = %q, want a", m.openID)
	}

	// Move down: the pane must follow to "c" and mark it read.
	m, cmd := step(t, m, keyPress('j'))
	if m.openID != "c" {
		t.Fatalf("openID = %q after moving down, want the pane to follow to c", m.openID)
	}
	if cmd == nil {
		t.Fatal("following the selection returned nil cmd, want a body-load + mark-read")
	}
	runCmdMsgs(t, cmd())

	items, err := st.Query(context.Background(), model.Filter{})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	for _, it := range items {
		if it.ID == "c" && !it.Read {
			t.Error("item c not marked read after the pane followed to it")
		}
	}

	// Re-pressing j at the bottom keeps the selection on c, so the pane must not
	// re-trigger a load/mark-read (openID unchanged).
	before := m.openID
	m, _ = step(t, m, keyPress('j'))
	if m.openID != before {
		t.Errorf("openID changed to %q on a no-op move, want it to stay %q", m.openID, before)
	}
}

func TestView_splitStacksFeedPaneAndStatusWithoutOverflow(t *testing.T) {
	m, _ := loadedModel(t) // 100x30
	m, _ = step(t, m, keyPress('p'))
	// Land a rendered body in the pane so its region is identifiable.
	m, _ = step(t, m, bodyLoadedMsg{id: "a", rendered: "PANE-BODY-MARKER"})

	v := m.View().Content
	if !strings.Contains(v, "Unread One") {
		t.Errorf("split view missing the feed:\n%s", v)
	}
	if !strings.Contains(v, "PANE-BODY-MARKER") {
		t.Errorf("split view missing the reading pane body:\n%s", v)
	}
	if !strings.Contains(v, "preview") {
		t.Errorf("split view missing the status/help line:\n%s", v)
	}
	// The stacked layout must never exceed the terminal height (the overflow bug).
	if lines := strings.Count(v, "\n") + 1; lines > m.h {
		t.Errorf("split view is %d lines, exceeds terminal height %d:\n%s", lines, m.h, v)
	}
}

func TestOpenFromPane_promotesToReaderWithoutReload(t *testing.T) {
	m, _ := loadedModel(t)
	m, _ = step(t, m, keyPress('p'))                                      // pane open on "a"
	m, _ = step(t, m, bodyLoadedMsg{id: "a", rendered: "KEEP-ME-MARKER"}) // body already loaded

	m, cmd := step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.view != viewReader {
		t.Errorf("view = %d after Enter, want viewReader", m.view)
	}
	if cmd != nil {
		t.Error("promoting the already-previewed item returned a cmd, want nil (no clear/reload)")
	}
	if got := m.reader.View(); !strings.Contains(got, "KEEP-ME-MARKER") {
		t.Errorf("reader body was cleared on promote, want it preserved:\n%s", got)
	}
}

func TestBackFromReader_restoresOpenSplit(t *testing.T) {
	m, _ := loadedModel(t)
	m, _ = step(t, m, keyPress('p'))
	m, _ = step(t, m, bodyLoadedMsg{id: "a", rendered: "PANE-BODY-MARKER"})
	m, _ = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // promote to full reader

	m, _ = step(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // back to feed
	if m.view != viewFeed {
		t.Errorf("view = %d after Esc, want viewFeed", m.view)
	}
	if !m.split {
		t.Error("split cleared on Esc, want the pane restored")
	}
	v := m.View().Content
	if !strings.Contains(v, "Unread One") || !strings.Contains(v, "PANE-BODY-MARKER") {
		t.Errorf("restored split missing feed or pane:\n%s", v)
	}
}

func TestReadToggled_paneOpenUnderUnreadFilter_keepsRowAndStaysSynced(t *testing.T) {
	m, _ := twoUnreadModel(t)
	m.filter.Read = model.ReadUnreadOnly // unread-only: the common RSS reading mode

	m, _ = step(t, m, keyPress('p')) // open the pane on "a"
	if !m.split || m.openID != "a" {
		t.Fatalf("precondition split=%v openID=%q, want true/a", m.split, m.openID)
	}

	// The persisted read confirmation for the previewed item arrives. With the pane
	// open it must NOT remove the row (which would desync the live pane and cascade the
	// whole unread list away); the row stays, dimmed, and the pane keeps tracking it.
	m, cmd := step(t, m, readToggledMsg{id: "a", read: true})
	if cmd != nil {
		t.Errorf("readToggledMsg under an open pane returned a cmd %v, want nil (no re-query)", cmd)
	}
	if m.openID != "a" {
		t.Errorf("openID = %q after read-confirm, want it to stay a (pane in sync)", m.openID)
	}
	if v := m.feed.View(); !strings.Contains(v, "Unread One") {
		t.Errorf("row was removed under the open pane, want it kept in place:\n%s", v)
	}

	// Sanity: with the pane CLOSED the original remove-on-read behavior is preserved.
	m2, _ := twoUnreadModel(t)
	m2.filter.Read = model.ReadUnreadOnly
	m2, _ = step(t, m2, readToggledMsg{id: "a", read: true})
	if v := m2.feed.View(); strings.Contains(v, "Unread One") {
		t.Errorf("with the pane closed the read row should be removed under unread-only:\n%s", v)
	}
}

func TestPreview_singleFlight_coalescesConcurrentRenders(t *testing.T) {
	m, _ := twoUnreadModel(t)

	// Open the pane on "a": one render is now in flight.
	m, _ = step(t, m, keyPress('p'))
	if !m.loading {
		t.Fatal("loading not set after opening the pane, want a render in flight")
	}

	// Move to "c" while the first render is still running. The pane must follow
	// (openID, header) and mark "c" read, but must NOT spawn a second render — the gate
	// coalesces a scroll burst to one render at a time (the renderer is lock-safe, but
	// rendering every fly-over row would be wasted work).
	m, _ = step(t, m, keyPress('j'))
	if m.openID != "c" {
		t.Fatalf("openID = %q after moving, want c", m.openID)
	}
	if !m.loading {
		t.Error("loading cleared while a render is still in flight (single-flight broken)")
	}

	// The first (now stale) render finishes. Its content must be discarded, and the
	// trailing edge must kick off the load for the current selection ("c").
	m, cmd := step(t, m, bodyLoadedMsg{id: "a", rendered: "STALE-BODY-A"})
	if got := m.reader.View(); strings.Contains(got, "STALE-BODY-A") {
		t.Errorf("stale body for a leaked into the pane:\n%s", got)
	}
	if cmd == nil {
		t.Fatal("stale completion returned nil cmd, want the trailing load for c")
	}
	if !m.loading {
		t.Error("loading not set for the trailing render, want single-flight re-armed")
	}

	// The current selection's render finishes and lands in the pane; the gate frees.
	m, _ = step(t, m, bodyLoadedMsg{id: "c", rendered: "FRESH-BODY-C"})
	if m.loading {
		t.Error("loading still set after the current render completed")
	}
	if got := m.reader.View(); !strings.Contains(got, "FRESH-BODY-C") {
		t.Errorf("current body for c missing from the pane:\n%s", got)
	}
}
