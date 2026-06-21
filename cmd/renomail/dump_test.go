package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/source"
	"github.com/srhoton/renomail/internal/source/rss"
	"github.com/srhoton/renomail/internal/store"
)

const rssBody = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
  <title>Dump Feed</title>
  <item><title>Older</title><link>https://x/1</link><guid>g1</guid>
    <pubDate>Tue, 02 Jan 2024 10:00:00 GMT</pubDate><description>one</description></item>
  <item><title>Newer</title><link>https://x/2</link><guid>g2</guid>
    <pubDate>Wed, 03 Jan 2024 10:00:00 GMT</pubDate><description>two</description></item>
</channel></rss>`

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dump.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestDumpFeeds_persistsAndPrintsNewestFirst(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(rssBody))
	}))
	t.Cleanup(srv.Close)

	st := newStore(t)
	p := rss.New(rss.NewFeedRef(srv.URL, "Dump Feed").Source, srv.URL, srv.Client())

	var buf bytes.Buffer
	if err := dumpFeeds(ctx, &buf, io.Discard, st, []source.Provider{p}); err != nil {
		t.Fatalf("dumpFeeds: %v", err)
	}

	// Items must be persisted.
	items, err := st.Query(ctx, model.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("stored %d items, want 2", len(items))
	}

	// Output is newest-first: "Newer" before "Older".
	out := buf.String()
	iNewer := strings.Index(out, "Newer")
	iOlder := strings.Index(out, "Older")
	if iNewer < 0 || iOlder < 0 || iNewer > iOlder {
		t.Errorf("output not newest-first:\n%s", out)
	}

	// The refreshed source state must be persisted with a LastSync.
	src, ok, err := st.GetSource(ctx, p.ID())
	if err != nil || !ok {
		t.Fatalf("GetSource ok=%v err=%v", ok, err)
	}
	if src.LastSync.IsZero() {
		t.Error("source LastSync not persisted")
	}
}

func TestDumpFeeds_notModified_isNoOp(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"e1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"e1"`)
		_, _ = w.Write([]byte(rssBody))
	}))
	t.Cleanup(srv.Close)

	st := newStore(t)
	p := rss.New(rss.NewFeedRef(srv.URL, "Dump Feed").Source, srv.URL, srv.Client())

	// First run captures the ETag and stores items.
	if err := dumpFeeds(ctx, &bytes.Buffer{}, io.Discard, st, []source.Provider{p}); err != nil {
		t.Fatalf("first dumpFeeds: %v", err)
	}
	// Rebuild the provider with the captured validator (mimicking the registry).
	src, _, _ := st.GetSource(ctx, p.ID())
	p2 := rss.New(src, srv.URL, srv.Client())

	// Second run should hit 304, not error, and report "not modified" on errOut.
	var errOut bytes.Buffer
	if err := dumpFeeds(ctx, &bytes.Buffer{}, &errOut, st, []source.Provider{p2}); err != nil {
		t.Fatalf("second dumpFeeds: %v", err)
	}
	if got := p2.SourceState().ETag; got != `"e1"` {
		t.Errorf("ETag after 304 = %q, want preserved %q", got, `"e1"`)
	}
	if !strings.Contains(errOut.String(), "not modified") {
		t.Errorf("errOut = %q, want a 'not modified' notice", errOut.String())
	}
}

// TestRunDump_endToEnd drives the full dispatch -> runDump path: it resolves
// paths, loads a config with a one-off feed pointing at a live test server,
// creates the data dir, opens the store, fetches, upserts, and prints.
func TestRunDump_endToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(rssBody))
	}))
	t.Cleanup(srv.Close)

	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	dir := filepath.Join(cfgHome, "renomail")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	content := "[[feed]]\nurl = \"" + srv.URL + "\"\ntitle = \"E2E\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var buf bytes.Buffer
	if err := dispatch(context.Background(), []string{"dump"}, &buf); err != nil {
		t.Fatalf("dispatch(dump) error = %v", err)
	}
	if !strings.Contains(buf.String(), "Newer") || !strings.Contains(buf.String(), "Older") {
		t.Errorf("dump output missing items:\n%s", buf.String())
	}
}

// mockEmailProvider is a source.Provider with no SourceState method, standing in
// for the Gmail provider so dumpFeeds can be exercised across both source
// families without a network or OAuth dependency.
type mockEmailProvider struct {
	id, name string
	items    []model.Item
}

func (m mockEmailProvider) ID() string       { return m.id }
func (m mockEmailProvider) Name() string     { return m.name }
func (m mockEmailProvider) Kind() model.Kind { return model.KindEmail }
func (m mockEmailProvider) Fetch(context.Context, time.Time) ([]model.Item, error) {
	return m.items, nil
}
func (m mockEmailProvider) Body(context.Context, *model.Item) error { return nil }

func TestDumpFeeds_interleavesEmailAndRSS_persistsMinimalSourceState(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(rssBody))
	}))
	t.Cleanup(srv.Close)

	st := newStore(t)
	rssP := rss.New(rss.NewFeedRef(srv.URL, "Dump Feed").Source, srv.URL, srv.Client())
	// An email dated between the two RSS items, to prove interleaving by date.
	mid := time.Date(2024, time.January, 2, 18, 0, 0, 0, time.UTC)
	mailP := mockEmailProvider{
		id:   "gmail:me@example.com",
		name: "me@example.com",
		items: []model.Item{{
			ID: model.StableID("gmail:me@example.com", "m1"), Kind: model.KindEmail,
			SourceID: "gmail:me@example.com", SourceName: "me@example.com",
			Title: "Midmail", NativeID: "m1", Published: mid,
		}},
	}

	var buf bytes.Buffer
	if err := dumpFeeds(ctx, &buf, io.Discard, st, []source.Provider{rssP, mailP}); err != nil {
		t.Fatalf("dumpFeeds: %v", err)
	}

	// All three items present, ordered newest-first: Newer, Midmail, Older.
	out := buf.String()
	iNewer, iMid, iOlder := strings.Index(out, "Newer"), strings.Index(out, "Midmail"), strings.Index(out, "Older")
	if iNewer < 0 || iMid < 0 || iOlder < 0 || !(iNewer < iMid && iMid < iOlder) {
		t.Errorf("items not interleaved newest-first:\n%s", out)
	}

	// The email provider has no SourceState method, so dumpFeeds must persist a
	// minimal Source carrying id/name/kind and a LastSync.
	src, ok, err := st.GetSource(ctx, mailP.ID())
	if err != nil || !ok {
		t.Fatalf("GetSource(email) ok=%v err=%v", ok, err)
	}
	if src.Kind != model.KindEmail || src.Name != mailP.Name() || src.LastSync.IsZero() {
		t.Errorf("minimal source state not persisted: %+v", src)
	}
}

// errWriter fails every Write, to exercise the print-loop error path.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestDumpFeeds_writeError_propagates(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(rssBody))
	}))
	t.Cleanup(srv.Close)

	st := newStore(t)
	p := rss.New(rss.NewFeedRef(srv.URL, "Dump Feed").Source, srv.URL, srv.Client())
	if err := dumpFeeds(ctx, errWriter{}, io.Discard, st, []source.Provider{p}); err == nil {
		t.Fatal("dumpFeeds() error = nil, want write error to propagate")
	}
}

func TestDumpFeeds_fetchError_skipsAndContinues(t *testing.T) {
	ctx := context.Background()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(rssBody))
	}))
	t.Cleanup(good.Close)

	st := newStore(t)
	pBad := rss.New(rss.NewFeedRef(bad.URL, "Bad").Source, bad.URL, bad.Client())
	pGood := rss.New(rss.NewFeedRef(good.URL, "Good").Source, good.URL, good.Client())

	var buf, errOut bytes.Buffer
	if err := dumpFeeds(ctx, &buf, &errOut, st, []source.Provider{pBad, pGood}); err != nil {
		t.Fatalf("dumpFeeds should not fail when one feed errors: %v", err)
	}
	items, _ := st.Query(ctx, model.Filter{})
	if len(items) != 2 {
		t.Fatalf("good feed items missing: got %d, want 2", len(items))
	}
	if !strings.Contains(errOut.String(), "warn: Bad:") {
		t.Errorf("errOut = %q, want a warning for the failed feed", errOut.String())
	}
}
