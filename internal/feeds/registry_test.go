package feeds

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/srhoton/renomail/internal/config"
	"github.com/srhoton/renomail/internal/source/rss"
	"github.com/srhoton/renomail/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

const opmlFixture = `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0"><body>
  <outline text="A" title="A" type="rss" xmlUrl="https://a.example/feed.xml"/>
  <outline text="Cat" title="Cat">
    <outline text="B" title="B" type="rss" xmlUrl="https://b.example/feed.xml"/>
  </outline>
</body></opml>`

func writeOPML(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "feeds.opml")
	if err := os.WriteFile(path, []byte(opmlFixture), 0o600); err != nil {
		t.Fatalf("write opml: %v", err)
	}
	return path
}

func TestBuildRSSProviders_hydratesStoredValidators(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	opmlPath := writeOPML(t)

	// Pre-seed feed A's stored source with a validator the registry must restore.
	aRef := rss.NewFeedRef("https://a.example/feed.xml", "stale-name")
	stored := aRef.Source
	stored.ETag = `"persisted"`
	stored.LastModified = "Mon, 02 Jan 2006 15:04:05 GMT"
	if err := st.UpsertSource(ctx, stored); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	cfg := config.Config{OPML: []config.OPMLSource{{Path: opmlPath}}}
	providers, err := BuildRSSProviders(ctx, cfg, st, nil)
	if err != nil {
		t.Fatalf("BuildRSSProviders: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("got %d providers, want 2", len(providers))
	}

	byID := map[string]*rss.Provider{}
	for _, p := range providers {
		byID[p.ID()] = p
	}
	a := byID[rss.NewFeedRef("https://a.example/feed.xml", "").Source.ID]
	if a == nil {
		t.Fatal("feed A provider missing")
	}
	if st := a.SourceState(); st.ETag != `"persisted"` || st.LastModified != "Mon, 02 Jan 2006 15:04:05 GMT" {
		t.Errorf("feed A validators = %q / %q, want hydrated from store", st.ETag, st.LastModified)
	}
	// The fresh OPML name wins over the stored one.
	if a.Name() != "A" {
		t.Errorf("feed A name = %q, want fresh OPML name %q", a.Name(), "A")
	}
}

func TestBuildRSSProviders_includesOneOffFeedsAndDedupes(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	opmlPath := writeOPML(t)

	cfg := config.Config{
		OPML: []config.OPMLSource{{Path: opmlPath}},
		Feed: []config.FeedSource{
			{URL: "https://c.example/feed.xml", Title: "C"},
			{URL: "https://a.example/feed.xml", Title: "dup of A"}, // duplicate ID
		},
	}
	providers, err := BuildRSSProviders(ctx, cfg, st, nil)
	if err != nil {
		t.Fatalf("BuildRSSProviders: %v", err)
	}
	// A, B (OPML) + C (one-off); the duplicate of A collapses.
	if len(providers) != 3 {
		t.Fatalf("got %d providers, want 3 (dedup failed?)", len(providers))
	}

	ids := map[string]bool{}
	for _, p := range providers {
		if ids[p.ID()] {
			t.Errorf("duplicate provider ID %q", p.ID())
		}
		ids[p.ID()] = true
	}
	cID := rss.NewFeedRef("https://c.example/feed.xml", "").Source.ID
	if !ids[cID] {
		t.Error("one-off feed C missing from providers")
	}
}

func TestBuildRSSProviders_unknownSource_noValidators(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	cfg := config.Config{Feed: []config.FeedSource{{URL: "https://new.example/feed.xml", Title: "New"}}}

	providers, err := BuildRSSProviders(ctx, cfg, st, nil)
	if err != nil {
		t.Fatalf("BuildRSSProviders: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("got %d providers, want 1", len(providers))
	}
	if got := providers[0].SourceState(); got.ETag != "" || got.LastModified != "" {
		t.Errorf("unseen source carried validators: %+v", got)
	}
}

func TestBuildRSSProviders_badOPMLPath_errors(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	cfg := config.Config{OPML: []config.OPMLSource{{Path: filepath.Join(t.TempDir(), "missing.opml")}}}
	if _, err := BuildRSSProviders(ctx, cfg, st, nil); err == nil {
		t.Fatal("BuildRSSProviders() error = nil, want error for missing OPML")
	}
}
