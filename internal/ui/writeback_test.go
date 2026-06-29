package ui

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/source"
)

// recordingSyncer is a source.Provider that also implements source.ReadSyncer,
// recording every SetRead call so tests can assert the UI pushed read-state changes
// back to the source.
type recordingSyncer struct {
	stubProvider
	mu    sync.Mutex
	calls []syncCall
}

type syncCall struct {
	ids  []string
	read bool
}

func (r *recordingSyncer) SetRead(_ context.Context, ids []string, read bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, syncCall{ids: slices.Clone(ids), read: read})
	return nil
}

func (r *recordingSyncer) snapshot() []syncCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// runCmd executes a command tree (descending into tea.Batch) and returns the leaf
// messages, so a test can drive the side effects of a batched command synchronously.
func runCmd(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	switch v := cmd().(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range v {
			out = append(out, runCmd(t, c)...)
		}
		return out
	case nil:
		return nil
	default:
		return []tea.Msg{v}
	}
}

func TestSyncReadFor_selectsReadSyncer(t *testing.T) {
	rec := &recordingSyncer{stubProvider: stubProvider{id: "gmail:me"}}
	plain := stubProvider{id: "rss:feed"} // implements Provider, not ReadSyncer
	m, err := New(newSeededStore(t), nil, []source.Provider{rec, plain}, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	cases := []struct {
		name    string
		item    model.Item
		wantCmd bool
	}{
		{"read-syncer source", model.Item{SourceID: "gmail:me", NativeID: "m1"}, true},
		{"non-syncer source", model.Item{SourceID: "rss:feed", NativeID: "g1"}, false},
		{"unknown source", model.Item{SourceID: "applemail:x", NativeID: "m2"}, false},
		{"no native id", model.Item{SourceID: "gmail:me", NativeID: ""}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := m.syncReadFor(c.item, true)
			if (cmd != nil) != c.wantCmd {
				t.Fatalf("syncReadFor cmd != nil = %v, want %v", cmd != nil, c.wantCmd)
			}
		})
	}
}

func TestToggleRead_pushesToSource(t *testing.T) {
	rec := &recordingSyncer{stubProvider: stubProvider{id: "gmail:me"}}
	m, err := New(newSeededStore(t), nil, []source.Provider{rec}, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	item := model.Item{
		ID: "e1", Kind: model.KindEmail, SourceID: "gmail:me", SourceName: "me",
		NativeID: "m1", Read: false, Published: time.Now(),
	}
	m, _ = step(t, m, itemsLoadedMsg{items: []model.Item{item}})

	_, cmd := step(t, m, tea.KeyPressMsg{Code: 'm', Text: "m"})
	runCmd(t, cmd) // drive the batched store write + write-back

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d SetRead calls, want 1", len(calls))
	}
	if !calls[0].read || len(calls[0].ids) != 1 || calls[0].ids[0] != "m1" {
		t.Errorf("SetRead call = %+v, want read=true ids=[m1]", calls[0])
	}
}

func TestPaneFollow_deliberateOpenSyncs_flyoverDoesNot(t *testing.T) {
	rec := &recordingSyncer{stubProvider: stubProvider{id: "gmail:me"}}
	st := newSeededStore(t)
	now := time.Now()
	items := []model.Item{
		{ID: "e1", Kind: model.KindEmail, SourceID: "gmail:me", NativeID: "n1", Read: false, Published: now.Add(-1 * time.Minute)},
		{ID: "e2", Kind: model.KindEmail, SourceID: "gmail:me", NativeID: "n2", Read: false, Published: now.Add(-2 * time.Minute)},
	}
	if _, err := st.UpsertItems(context.Background(), items); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	m, err := New(st, nil, []source.Provider{rec}, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = step(t, m, itemsLoadedMsg{items: items})

	// Open the pane on the selected (newest) row: a deliberate open, which syncs.
	m, cmd := step(t, m, keyPress('p'))
	runCmd(t, cmd)
	if calls := rec.snapshot(); len(calls) != 1 || calls[0].ids[0] != "n1" {
		t.Fatalf("deliberate pane-open calls = %+v, want one push of n1", calls)
	}

	// Move down: the pane follows to e2 and marks it read LOCALLY, but a passive fly-over
	// must NOT push to the source (no per-row subprocess/API swarm).
	_, cmd = step(t, m, keyPress('j'))
	runCmd(t, cmd)
	if calls := rec.snapshot(); len(calls) != 1 {
		t.Errorf("fly-over added a write-back: calls = %+v, want still just 1 (n1)", calls)
	}
	read, err := st.Query(context.Background(), model.Filter{Read: model.ReadReadOnly})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	gotLocalRead := map[string]bool{}
	for _, it := range read {
		gotLocalRead[it.ID] = true
	}
	if !gotLocalRead["e2"] {
		t.Error("fly-over should still mark e2 read locally, even though it is not synced")
	}
}

func TestMarkAllReadCmd_skipsWriteBackUnderReadOnlyFilter(t *testing.T) {
	st := newSeededStore(t)
	items := []model.Item{
		{ID: "u1", Kind: model.KindEmail, SourceID: "gmail:me", NativeID: "n1", Read: false, Published: time.Now()},
		{ID: "r1", Kind: model.KindEmail, SourceID: "gmail:me", NativeID: "n2", Read: true, Published: time.Now()},
	}
	if _, err := st.UpsertItems(context.Background(), items); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	// Mark All Read while filtered to read-only flips nothing (everything shown is already
	// read), so it must NOT push the unread u1/n1 to the source.
	msg := markAllReadCmd(st, model.Filter{Read: model.ReadReadOnly})()
	marked, ok := msg.(markedAllReadMsg)
	if !ok {
		t.Fatalf("got %T, want markedAllReadMsg", msg)
	}
	if marked.err != nil {
		t.Fatalf("err = %v", marked.err)
	}
	if len(marked.bySource) != 0 {
		t.Errorf("bySource = %v, want empty under a read-only filter (nothing flipped)", marked.bySource)
	}
}

func TestMarkAllReadCmd_groupsUnreadNativeIDsBySource(t *testing.T) {
	st := newSeededStore(t)
	items := []model.Item{
		{ID: "g1", Kind: model.KindEmail, SourceID: "gmail:me", NativeID: "n1", Read: false, Published: time.Now()},
		{ID: "g2", Kind: model.KindEmail, SourceID: "gmail:me", NativeID: "n2", Read: false, Published: time.Now()},
		{ID: "a1", Kind: model.KindEmail, SourceID: "applemail:x", NativeID: "<n3@x>", Read: false, Published: time.Now()},
		{ID: "r1", Kind: model.KindEmail, SourceID: "gmail:me", NativeID: "n4", Read: true, Published: time.Now()},
	}
	if _, err := st.UpsertItems(context.Background(), items); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}

	msg := markAllReadCmd(st, model.Filter{})()
	marked, ok := msg.(markedAllReadMsg)
	if !ok {
		t.Fatalf("markAllReadCmd produced %T, want markedAllReadMsg", msg)
	}
	if marked.err != nil {
		t.Fatalf("markedAllReadMsg.err = %v", marked.err)
	}
	// Only the currently-unread items are reported, grouped by source; the already-read
	// g1/n4 is excluded.
	if got := marked.bySource["gmail:me"]; len(got) != 2 {
		t.Errorf("gmail group = %v, want 2 unread ids", got)
	}
	if got := marked.bySource["applemail:x"]; len(got) != 1 || got[0] != "<n3@x>" {
		t.Errorf("applemail group = %v, want [<n3@x>]", got)
	}
}
