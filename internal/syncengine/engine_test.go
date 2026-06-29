package syncengine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/source"
	"github.com/srhoton/renomail/internal/store"
)

// mockProvider is an in-memory source.Provider for engine tests: it returns a
// fixed item set (or an error), records the since it was fetched with, and tracks
// peak concurrency via shared atomics so the bounded fan-out can be asserted.
type mockProvider struct {
	id, name string
	items    []model.Item
	err      error
	delay    time.Duration

	gotSince atomic.Int64 // unix nanos of the since passed to Fetch

	inflight *atomic.Int32 // shared across providers in one test
	peak     *atomic.Int32
}

func (m *mockProvider) ID() string       { return m.id }
func (m *mockProvider) Name() string     { return m.name }
func (m *mockProvider) Kind() model.Kind { return model.KindEmail }

func (m *mockProvider) Fetch(ctx context.Context, since time.Time) ([]model.Item, error) {
	m.gotSince.Store(since.UnixNano())
	if m.inflight != nil {
		cur := m.inflight.Add(1)
		for {
			old := m.peak.Load()
			if cur <= old || m.peak.CompareAndSwap(old, cur) {
				break
			}
		}
		defer m.inflight.Add(-1)
	}
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// Return items and err together so a provider can model a partial fetch
	// (some items harvested before an error), which the engine must still upsert.
	return m.items, m.err
}

func (m *mockProvider) Body(context.Context, *model.Item) error { return nil }

var _ source.Provider = (*mockProvider)(nil)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "engine.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func item(sourceID, native, title string, published time.Time) model.Item {
	return model.Item{
		ID:         model.StableID(sourceID, native),
		Kind:       model.KindEmail,
		SourceID:   sourceID,
		SourceName: sourceID,
		Title:      title,
		NativeID:   native,
		Published:  published,
	}
}

// drain reads exactly n results from the engine's events channel, failing if they
// do not arrive promptly.
func drain(t *testing.T, e *Engine, n int) []Result {
	t.Helper()
	out := make([]Result, 0, n)
	for range n {
		select {
		case r := <-e.Events():
			out = append(out, r)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for result %d/%d", len(out)+1, n)
		}
	}
	return out
}

func TestSyncAll_upsertsAllAndEmitsOnePerProvider(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC()

	pA := &mockProvider{id: "a", name: "A", items: []model.Item{item("a", "a1", "Alpha", now)}}
	pB := &mockProvider{id: "b", name: "B", items: []model.Item{
		item("b", "b1", "Bravo", now), item("b", "b2", "Bravo2", now.Add(-time.Hour)),
	}}
	e := New([]source.Provider{pA, pB}, st, time.Hour)

	e.syncAll(ctx, false)
	results := drain(t, e, 2)

	if len(results) != 2 {
		t.Fatalf("got %d results, want one per provider (2)", len(results))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("provider %s: unexpected err %v", r.SourceName, r.Err)
		}
	}

	all, err := st.Query(ctx, model.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("stored %d items, want 3 (all providers' items upserted)", len(all))
	}
}

func TestSyncAll_reportsInsertedCount(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC()

	p := &mockProvider{id: "a", name: "A", items: []model.Item{
		item("a", "a1", "Alpha", now),
		item("a", "a2", "Bravo", now.Add(-time.Hour)),
	}}
	e := New([]source.Provider{p}, st, time.Hour)

	// First sweep: both items are new.
	e.syncAll(ctx, false)
	first := drain(t, e, 1)
	if first[0].Inserted != 2 {
		t.Errorf("first sweep Inserted = %d, want 2 (both items new)", first[0].Inserted)
	}

	// Second sweep re-returns the same two items plus one new one; only the new
	// one counts as inserted.
	p.items = append(p.items, item("a", "a3", "Charlie", now.Add(-2*time.Hour)))
	e.syncAll(ctx, false)
	second := drain(t, e, 1)
	if second[0].Inserted != 1 {
		t.Errorf("re-sync Inserted = %d, want 1 (only the new item counts)", second[0].Inserted)
	}
}

func TestSyncAll_errorReportsZeroInserted(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	// A pure failure (no harvested items) must report zero inserts.
	p := &mockProvider{id: "a", name: "A", err: errors.New("boom")}
	e := New([]source.Provider{p}, st, time.Hour)

	e.syncAll(ctx, false)
	r := drain(t, e, 1)
	if r[0].Inserted != 0 {
		t.Errorf("failed sweep Inserted = %d, want 0", r[0].Inserted)
	}
}

func TestSyncAll_oneError_othersStillUpserted(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC()

	boom := errors.New("fetch failed")
	bad := &mockProvider{id: "bad", name: "Bad", err: boom}
	good := &mockProvider{id: "good", name: "Good", items: []model.Item{item("good", "g1", "OK", now)}}
	e := New([]source.Provider{bad, good}, st, time.Hour)

	e.syncAll(ctx, false)
	results := drain(t, e, 2)

	byName := map[string]Result{}
	for _, r := range results {
		byName[r.SourceName] = r
	}
	if !errors.Is(byName["Bad"].Err, boom) {
		t.Errorf("Bad result err = %v, want %v", byName["Bad"].Err, boom)
	}
	if byName["Good"].Err != nil {
		t.Errorf("Good result err = %v, want nil (resilient to Bad's failure)", byName["Good"].Err)
	}

	all, err := st.Query(ctx, model.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 1 || all[0].SourceID != "good" {
		t.Fatalf("stored %d items, want only the good provider's 1 item", len(all))
	}
}

func TestSyncAll_fetchError_doesNotAdvanceLastSync(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	prior := time.Now().Add(-48 * time.Hour).Truncate(time.Second).UTC()
	if err := st.UpsertSource(ctx, model.Source{ID: "a", Name: "A", Kind: model.KindEmail, LastSync: prior}); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	p := &mockProvider{id: "a", name: "A", err: errors.New("boom")}
	e := New([]source.Provider{p}, st, time.Hour)

	e.syncAll(ctx, false)
	_ = drain(t, e, 1)

	src, ok, err := st.GetSource(ctx, "a")
	if err != nil || !ok {
		t.Fatalf("GetSource ok=%v err=%v", ok, err)
	}
	if !src.LastSync.Equal(prior) {
		t.Errorf("LastSync = %v, want left at %v (a failed fetch must not advance it)", src.LastSync, prior)
	}
}

func TestSyncAll_partialItemsWithError_upsertedButNoAdvance(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC()

	prior := now.Add(-48 * time.Hour).Truncate(time.Second)
	if err := st.UpsertSource(ctx, model.Source{ID: "a", Name: "A", Kind: model.KindEmail, LastSync: prior}); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	// A provider that returns one harvested item alongside an error.
	p := &mockProvider{
		id: "a", name: "A",
		items: []model.Item{item("a", "a1", "Harvested", now)},
		err:   errors.New("one get failed"),
	}
	e := New([]source.Provider{p}, st, time.Hour)

	e.syncAll(ctx, false)
	_ = drain(t, e, 1)

	// The harvested item must be persisted despite the error.
	all, err := st.Query(ctx, model.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 1 || all[0].Title != "Harvested" {
		t.Fatalf("stored %d items, want the 1 harvested item kept", len(all))
	}
	// But LastSync must not advance, so the next sweep retries the failed window.
	src, _, _ := st.GetSource(ctx, "a")
	if !src.LastSync.Equal(prior) {
		t.Errorf("LastSync = %v, want left at %v on partial failure", src.LastSync, prior)
	}
}

func TestSyncAll_sinceIsStoredLastSync_andAdvances(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC()

	prior := now.Add(-48 * time.Hour).Truncate(time.Second)
	if err := st.UpsertSource(ctx, model.Source{ID: "a", Name: "A", Kind: model.KindEmail, LastSync: prior}); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	p := &mockProvider{id: "a", name: "A", items: []model.Item{item("a", "a1", "Alpha", now)}}
	e := New([]source.Provider{p}, st, time.Hour)

	e.syncAll(ctx, false)
	_ = drain(t, e, 1)

	if got := time.Unix(0, p.gotSince.Load()).UTC(); !got.Equal(prior) {
		t.Errorf("Fetch since = %v, want stored LastSync %v", got, prior)
	}

	src, ok, err := st.GetSource(ctx, "a")
	if err != nil || !ok {
		t.Fatalf("GetSource ok=%v err=%v", ok, err)
	}
	if !src.LastSync.After(prior) {
		t.Errorf("LastSync = %v, want advanced past %v", src.LastSync, prior)
	}
}

func TestSyncAll_concurrencyBounded(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	var inflight, peak atomic.Int32
	providers := make([]source.Provider, 0, 10)
	for i := range 10 {
		providers = append(providers, &mockProvider{
			id:       string(rune('a' + i)),
			name:     string(rune('A' + i)),
			delay:    20 * time.Millisecond,
			inflight: &inflight,
			peak:     &peak,
		})
	}
	e := New(providers, st, time.Hour)
	e.maxConc = 3 // tighten below the provider count so the bound is observable

	e.syncAll(ctx, false)
	_ = drain(t, e, 10)

	if got := peak.Load(); got > 3 {
		t.Errorf("peak concurrency = %d, want <= maxConc (3)", got)
	}
}

func TestTrigger_drivesAnExtraSweep(t *testing.T) {
	ctx := t.Context() // cancelled at test end, stopping Run cleanly
	st := newTestStore(t)
	now := time.Now().UTC()

	p := &mockProvider{id: "a", name: "A", items: []model.Item{item("a", "a1", "Alpha", now)}}
	e := New([]source.Provider{p}, st, time.Hour) // long interval: no automatic tick during the test

	go e.Run(ctx)

	// Drain the immediate first sweep.
	if got := drain(t, e, 1); got[0].SourceName != "A" {
		t.Fatalf("first result from %q, want A", got[0].SourceName)
	}

	// A manual trigger must drive a second sweep without waiting for the ticker.
	e.Trigger()
	select {
	case r := <-e.Events():
		if r.SourceName != "A" {
			t.Errorf("triggered result from %q, want A", r.SourceName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Trigger did not drive an extra sweep")
	}
}

func TestTrigger_nonBlockingWhenFull(t *testing.T) {
	st := newTestStore(t)
	// Run is never started, so the buffered trigger channel fills after one send and
	// stays full; subsequent Triggers must coalesce rather than block the caller.
	e := New([]source.Provider{&mockProvider{id: "a", name: "A"}}, st, time.Hour)

	done := make(chan struct{})
	go func() {
		for range 100 {
			e.Trigger()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Trigger blocked when its buffer was full")
	}
}

func TestRun_cancelClosesChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st := newTestStore(t)
	now := time.Now().UTC()

	p := &mockProvider{id: "a", name: "A", items: []model.Item{item("a", "a1", "Alpha", now)}}
	e := New([]source.Provider{p}, st, time.Hour) // long interval: no tick during the test

	go e.Run(ctx)

	// The immediate first sweep emits one result.
	select {
	case r := <-e.Events():
		if r.SourceName != "A" {
			t.Errorf("first result from %q, want A", r.SourceName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no result from initial sweep")
	}

	cancel()

	// After cancel, Run must close the channel; receive until !ok.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-e.Events():
			if !ok {
				return // closed cleanly
			}
		case <-deadline:
			t.Fatal("channel not closed after context cancel")
		}
	}
}

// digestRecorder is a DigestFunc that records the calls and items it receives and
// returns a configurable error. Its fields are mutex-guarded because the engine now
// posts the digest from a separate goroutine, so snapshot may race the notify call;
// notified signals each invocation so a test can wait for the async post.
type digestRecorder struct {
	mu       sync.Mutex
	calls    int
	items    []model.Item
	err      error
	notified chan struct{}
}

func newDigestRecorder(err error) *digestRecorder {
	return &digestRecorder{err: err, notified: make(chan struct{}, 8)}
}

func (d *digestRecorder) notify(_ context.Context, items []model.Item) error {
	d.mu.Lock()
	d.calls++
	d.items = append(d.items, items...)
	err := d.err
	d.mu.Unlock()
	select {
	case d.notified <- struct{}{}:
	default:
	}
	return err
}

func (d *digestRecorder) snapshot() (int, []model.Item) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls, slices.Clone(d.items)
}

// waitNotified blocks until the digest has been posted, failing if it does not
// happen promptly.
func (d *digestRecorder) waitNotified(t *testing.T) {
	t.Helper()
	select {
	case <-d.notified:
	case <-time.After(2 * time.Second):
		t.Fatal("digest was not posted")
	}
}

func TestSyncAll_digestCoalescesNewItemsAcrossSources(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC()

	pA := &mockProvider{id: "a", name: "A", items: []model.Item{item("a", "a1", "Alpha", now)}}
	pB := &mockProvider{id: "b", name: "B", items: []model.Item{item("b", "b1", "Bravo", now)}}
	e := New([]source.Provider{pA, pB}, st, time.Hour)

	rec := newDigestRecorder(nil)
	e.SetDigestNotifier(rec.notify)

	// A steady-state sweep posts one coalesced digest asynchronously; drain the two
	// provider Results, then wait for the post to land.
	e.syncAll(ctx, false)
	_ = drain(t, e, 2)
	rec.waitNotified(t)

	calls, got := rec.snapshot()
	if calls != 1 {
		t.Fatalf("digest calls = %d, want exactly 1 per sweep", calls)
	}
	if len(got) != 2 {
		t.Fatalf("digest items = %d, want 2 (one per source)", len(got))
	}
	titles := map[string]bool{}
	for _, it := range got {
		titles[it.Title] = true
	}
	if !titles["Alpha"] || !titles["Bravo"] {
		t.Errorf("digest items = %v, want both Alpha and Bravo", got)
	}
}

func TestSyncAll_digestSkippedOnInitialSweep(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC()

	p := &mockProvider{id: "a", name: "A", items: []model.Item{item("a", "a1", "Alpha", now)}}
	e := New([]source.Provider{p}, st, time.Hour)

	rec := newDigestRecorder(nil)
	e.SetDigestNotifier(rec.notify)

	// The initial sweep backfills and must never trigger the digest, even though it
	// inserts new rows.
	e.syncAll(ctx, true)
	_ = drain(t, e, 1)

	select {
	case <-rec.notified:
		t.Fatal("digest must be skipped on the initial sweep")
	case <-time.After(200 * time.Millisecond):
	}
	if calls, _ := rec.snapshot(); calls != 0 {
		t.Errorf("digest calls on initial sweep = %d, want 0", calls)
	}
}

func TestSyncAll_digestErrorSurfacedAsResult(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC()

	pA := &mockProvider{id: "a", name: "A", items: []model.Item{item("a", "a1", "Alpha", now)}}
	pB := &mockProvider{id: "b", name: "B", items: []model.Item{item("b", "b1", "Bravo", now)}}
	e := New([]source.Provider{pA, pB}, st, time.Hour)

	boom := errors.New("slack webhook: status 500")
	rec := newDigestRecorder(boom)
	e.SetDigestNotifier(rec.notify)

	// The error digest Result is emitted (from the async post goroutine) after the
	// two provider Results, past the channel buffer; drain all three.
	e.syncAll(ctx, false)
	results := drain(t, e, 3)

	var slack *Result
	for i := range results {
		if results[i].SourceName == "Slack" {
			slack = &results[i]
		}
	}
	if slack == nil {
		t.Fatalf("no Slack digest Result emitted; got %+v", results)
	}
	if !errors.Is(slack.Err, boom) {
		t.Errorf("Slack Result err = %v, want %v", slack.Err, boom)
	}
}

func TestSyncAll_noDigestWhenNotInstalled(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC()

	// Sanity: with no digest notifier, a steady-state sweep emits exactly one Result
	// per provider and nothing else.
	p := &mockProvider{id: "a", name: "A", items: []model.Item{item("a", "a1", "Alpha", now)}}
	e := New([]source.Provider{p}, st, time.Hour)

	e.syncAll(ctx, false)
	got := drain(t, e, 1)
	if got[0].Inserted != 1 {
		t.Errorf("Inserted = %d, want 1", got[0].Inserted)
	}
	select {
	case extra := <-e.Events():
		t.Errorf("unexpected extra Result: %+v", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

// alertRecorder is an AlertFunc that records the messages it receives and returns a
// configurable error. Its fields are mutex-guarded because the engine posts the
// threshold alert from a separate goroutine; notified signals each invocation so a
// test can wait for the async post.
type alertRecorder struct {
	mu       sync.Mutex
	msgs     []string
	err      error
	notified chan struct{}
}

func newAlertRecorder(err error) *alertRecorder {
	return &alertRecorder{err: err, notified: make(chan struct{}, 8)}
}

func (a *alertRecorder) notify(_ context.Context, msg string) error {
	a.mu.Lock()
	a.msgs = append(a.msgs, msg)
	err := a.err
	a.mu.Unlock()
	select {
	case a.notified <- struct{}{}:
	default:
	}
	return err
}

func (a *alertRecorder) snapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.msgs)
}

func (a *alertRecorder) waitNotified(t *testing.T) {
	t.Helper()
	select {
	case <-a.notified:
	case <-time.After(2 * time.Second):
		t.Fatal("threshold alert was not posted")
	}
}

// seedUnread inserts n unread items of the given kind into the store so the engine's
// threshold counts can be driven directly, independent of any provider.
func seedUnread(t *testing.T, st *store.Store, kind model.Kind, prefix string, n int) {
	t.Helper()
	now := time.Now().UTC()
	items := make([]model.Item, 0, n)
	for i := range n {
		native := fmt.Sprintf("%s-%s-%d", kind, prefix, i)
		items = append(items, model.Item{
			ID:         model.StableID(string(kind), native),
			Kind:       kind,
			SourceID:   string(kind),
			SourceName: string(kind),
			NativeID:   native,
			Published:  now.Add(-time.Duration(i) * time.Minute),
		})
	}
	if _, err := st.UpsertItems(context.Background(), items); err != nil {
		t.Fatalf("seed %s items: %v", kind, err)
	}
}

// newAlertEngine builds an engine with no providers (so syncAll exercises only the
// threshold path), the recorder installed, and low thresholds so a handful of seeded
// items crosses them.
func newAlertEngine(t *testing.T, st *store.Store, rec *alertRecorder) *Engine {
	t.Helper()
	e := New([]source.Provider{}, st, time.Hour)
	e.SetThresholdNotifier(rec.notify)
	e.emailThresh = 2
	e.rssThresh = 2
	return e
}

func TestSetThresholdNotifier_defaultsThresholds(t *testing.T) {
	st := newTestStore(t)
	e := New([]source.Provider{}, st, time.Hour)
	e.SetThresholdNotifier(func(context.Context, string) error { return nil })
	if e.emailThresh != defaultUnreadEmailThreshold || e.rssThresh != defaultUnreadRSSThreshold {
		t.Errorf("thresholds = (%d, %d), want defaults (%d, %d)",
			e.emailThresh, e.rssThresh, defaultUnreadEmailThreshold, defaultUnreadRSSThreshold)
	}
}

func TestMaybeAlert_firesOnceOnCrossing(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rec := newAlertRecorder(nil)
	e := newAlertEngine(t, st, rec)

	seedUnread(t, st, model.KindEmail, "a", 3) // 3 > emailThresh(2)

	// First steady-state sweep crosses the threshold and fires once.
	e.syncAll(ctx, false)
	rec.waitNotified(t)

	// A second sweep with the count still over must not refire (latched).
	e.syncAll(ctx, false)
	select {
	case <-rec.notified:
		t.Fatal("threshold alert refired while still latched over the threshold")
	case <-time.After(200 * time.Millisecond):
	}

	msgs := rec.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("alert messages = %d, want exactly 1", len(msgs))
	}
	if !strings.Contains(msgs[0], "3 unread emails") {
		t.Errorf("alert message = %q, want it to mention 3 unread emails", msgs[0])
	}
}

func TestMaybeAlert_reArmsAfterDroppingBelow(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rec := newAlertRecorder(nil)
	e := newAlertEngine(t, st, rec)

	seedUnread(t, st, model.KindEmail, "a", 3)
	e.syncAll(ctx, false)
	rec.waitNotified(t) // fire 1

	// Drop back under the threshold: the next sweep fires nothing but re-arms the latch.
	if err := st.MarkAllRead(ctx, model.Filter{}); err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
	e.syncAll(ctx, false)
	select {
	case <-rec.notified:
		t.Fatal("threshold alert fired while under the threshold")
	case <-time.After(200 * time.Millisecond):
	}

	// Cross again with fresh unread items: the re-armed latch fires a second time.
	seedUnread(t, st, model.KindEmail, "b", 3)
	e.syncAll(ctx, false)
	rec.waitNotified(t) // fire 2

	if msgs := rec.snapshot(); len(msgs) != 2 {
		t.Fatalf("alert messages = %d, want 2 (one per crossing)", len(msgs))
	}
}

func TestMaybeAlert_exactlyAtThresholdDoesNotFire(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rec := newAlertRecorder(nil)
	e := newAlertEngine(t, st, rec) // emailThresh = 2

	// Exactly at the threshold (2 unread, threshold 2) must NOT fire — the rule is
	// strictly greater-than. This pins the off-by-one boundary.
	seedUnread(t, st, model.KindEmail, "a", 2)

	e.syncAll(ctx, false)
	select {
	case <-rec.notified:
		t.Fatal("threshold alert fired at exactly the threshold; rule is strict >")
	case <-time.After(200 * time.Millisecond):
	}
	if msgs := rec.snapshot(); len(msgs) != 0 {
		t.Errorf("alert messages at threshold = %d, want 0", len(msgs))
	}
}

func TestMaybeAlert_skippedOnInitialSweep(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rec := newAlertRecorder(nil)
	e := newAlertEngine(t, st, rec)

	seedUnread(t, st, model.KindEmail, "a", 3) // over threshold

	// The initial backfill sweep must never fire, even with a standing backlog.
	e.syncAll(ctx, true)
	select {
	case <-rec.notified:
		t.Fatal("threshold alert must be skipped on the initial sweep")
	case <-time.After(200 * time.Millisecond):
	}
	if msgs := rec.snapshot(); len(msgs) != 0 {
		t.Errorf("alert messages on initial sweep = %d, want 0", len(msgs))
	}
}

func TestMaybeAlert_independentKindsCombineIntoOneMessage(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rec := newAlertRecorder(nil)
	e := newAlertEngine(t, st, rec)

	seedUnread(t, st, model.KindEmail, "a", 3) // > emailThresh(2)
	seedUnread(t, st, model.KindRSS, "a", 3)   // > rssThresh(2)

	e.syncAll(ctx, false)
	rec.waitNotified(t)

	msgs := rec.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("alert messages = %d, want exactly 1 combined message", len(msgs))
	}
	if !strings.Contains(msgs[0], "unread emails") || !strings.Contains(msgs[0], "unread RSS items") {
		t.Errorf("combined message = %q, want both kinds mentioned", msgs[0])
	}
}

func TestMaybeAlert_noNotifierIsNoOp(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	// No notifier installed: a steady-state sweep over the threshold must do nothing
	// (and, with no providers, emit no Results at all).
	seedUnread(t, st, model.KindEmail, "a", 50)
	e := New([]source.Provider{}, st, time.Hour)

	e.syncAll(ctx, false)
	select {
	case extra := <-e.Events():
		t.Errorf("unexpected Result with no notifier installed: %+v", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMaybeAlert_errorSurfacedAsResult(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	boom := errors.New("osascript: not permitted")
	rec := newAlertRecorder(boom)
	e := newAlertEngine(t, st, rec)

	seedUnread(t, st, model.KindEmail, "a", 3)

	e.syncAll(ctx, false)
	// The failed alert surfaces as a macOS Result on the events channel.
	select {
	case r := <-e.Events():
		if r.SourceName != "macOS" {
			t.Fatalf("Result SourceName = %q, want macOS", r.SourceName)
		}
		if !errors.Is(r.Err, boom) {
			t.Errorf("Result err = %v, want %v", r.Err, boom)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("threshold alert error was not surfaced as a Result")
	}
}
