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
	if err := st.UpsertItems(context.Background(), seedItems()); err != nil {
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
	if err := st.UpsertItems(ctx, []model.Item{bodyless}); err != nil {
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
	if err := st.UpsertItems(context.Background(), items); err != nil {
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

func TestHandleFeedKey_filterUnread_setsReadState(t *testing.T) {
	m, _ := loadedModel(t)
	m, cmd := step(t, m, keyPress('u'))
	if m.filter.Read != model.ReadUnreadOnly {
		t.Errorf("u set Read = %v, want ReadUnreadOnly", m.filter.Read)
	}
	if cmd == nil {
		t.Error("u returned nil cmd, want a re-query")
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

func TestUpdate_readToggled_unreadOnly_reQueries(t *testing.T) {
	m, _ := loadedModel(t)
	m.filter.Read = model.ReadUnreadOnly
	_, cmd := step(t, m, readToggledMsg{id: "a", read: true})
	if cmd == nil {
		t.Fatal("readToggledMsg under unread-only returned nil cmd, want re-query")
	}
	if _, ok := cmd().(itemsLoadedMsg); !ok {
		t.Error("readToggledMsg under unread-only did not re-query")
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
	if err := st.UpsertItems(context.Background(), seedItems()); err != nil {
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
