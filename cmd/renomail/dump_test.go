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

	"github.com/srhoton/renomail/internal/feeds"
	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/source/rss"
	"github.com/srhoton/renomail/internal/store"
)

const rssBody = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
  <title>Dump Feed</title>
  <item><title>Older</title><link>https://x/1</link><guid>g1</guid>
    <pubDate>Mon, 02 Jan 2006 10:00:00 GMT</pubDate><description>one</description></item>
  <item><title>Newer</title><link>https://x/2</link><guid>g2</guid>
    <pubDate>Tue, 03 Jan 2006 10:00:00 GMT</pubDate><description>two</description></item>
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
	if err := dumpFeeds(ctx, &buf, io.Discard, st, []*feeds.Provider{p}); err != nil {
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
	if err := dumpFeeds(ctx, &bytes.Buffer{}, io.Discard, st, []*feeds.Provider{p}); err != nil {
		t.Fatalf("first dumpFeeds: %v", err)
	}
	// Rebuild the provider with the captured validator (mimicking the registry).
	src, _, _ := st.GetSource(ctx, p.ID())
	p2 := rss.New(src, srv.URL, srv.Client())

	// Second run should hit 304, not error, and report "not modified" on errOut.
	var errOut bytes.Buffer
	if err := dumpFeeds(ctx, &bytes.Buffer{}, &errOut, st, []*feeds.Provider{p2}); err != nil {
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
	if err := dumpFeeds(ctx, errWriter{}, io.Discard, st, []*feeds.Provider{p}); err == nil {
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
	if err := dumpFeeds(ctx, &buf, &errOut, st, []*feeds.Provider{pBad, pGood}); err != nil {
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
