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
	"github.com/srhoton/renomail/internal/store"
)

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
	st, err := store.Open(filepath.Join(t.TempDir(), "ui.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertItems(context.Background(), seedItems()); err != nil {
		t.Fatalf("UpsertItems() error = %v", err)
	}
	m, err := New(st)
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

	msg := loadBodyCmd(st, m.renderer, model.Item{ID: "a"})()
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

	msg := loadBodyCmd(st, m.renderer, model.Item{ID: "does-not-exist"})()
	bl, ok := msg.(bodyLoadedMsg)
	if !ok {
		t.Fatalf("loadBodyCmd produced %T, want bodyLoadedMsg", msg)
	}
	if bl.err == nil {
		t.Error("loadBodyCmd err = nil, want error for missing item")
	}
}

func TestSetReadCmd_persistsReadFlag(t *testing.T) {
	_, st := newSeededModel(t)

	if out := setReadCmd(st, "a", true)(); out != nil {
		t.Errorf("setReadCmd msg = %v, want nil on success", out)
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
