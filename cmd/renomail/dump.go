package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/srhoton/renomail/internal/config"
	"github.com/srhoton/renomail/internal/feeds"
	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/source/rss"
	"github.com/srhoton/renomail/internal/store"
)

// runDump executes the full RSS pipeline once — import feeds, fetch each, upsert
// to the store, then print the merged feed — to prove the data path end to end
// before any TUI exists. It opens the store and builds the providers, delegating
// the work to dumpFeeds so the core loop is unit-testable.
func runDump(ctx context.Context, cfg config.Config, paths config.Paths, w io.Writer) error {
	// The data directory may not exist on a first run; the SQLite driver will
	// not create it, so ensure it is present before opening the database.
	if err := os.MkdirAll(paths.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir %s: %w", paths.DataDir, err)
	}
	st, err := store.Open(paths.DBFile)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	client := &http.Client{Timeout: rss.DefaultTimeout}
	providers, err := feeds.BuildRSSProviders(ctx, cfg, st, client)
	if err != nil {
		return err
	}
	return dumpFeeds(ctx, w, os.Stderr, st, providers)
}

// dumpFeeds fetches each provider, upserts its items and refreshed source state,
// then prints every stored item newest-first to out. Diagnostic lines (a skipped
// feed, or a feed unchanged since the last fetch) go to errOut. A fetch failure
// for one feed is reported and skipped rather than aborting the whole run.
func dumpFeeds(ctx context.Context, out, errOut io.Writer, st *store.Store, providers []*feeds.Provider) error {
	for _, p := range providers {
		items, err := p.Fetch(ctx, time.Time{})
		if err != nil {
			fmt.Fprintf(errOut, "warn: %s: %v\n", p.Name(), err)
			continue
		}
		// A 304 Not Modified yields a nil slice; an empty-but-changed feed
		// yields a non-nil empty slice, so nil distinguishes the two.
		if items == nil {
			fmt.Fprintf(errOut, "info: %s: not modified\n", p.Name())
		}
		if err := st.UpsertItems(ctx, items); err != nil {
			return fmt.Errorf("upsert %s: %w", p.Name(), err)
		}
		src := p.SourceState()
		src.LastSync = time.Now()
		if err := st.UpsertSource(ctx, src); err != nil {
			return fmt.Errorf("save source %s: %w", p.Name(), err)
		}
	}

	all, err := st.Query(ctx, model.Filter{})
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(out)
	for _, it := range all {
		// A bufio.Writer records the first write error and returns it from
		// Flush, so per-line errors are checked once at Flush below.
		_, _ = fmt.Fprintf(bw, "%s  [%s] %-24s  %s\n",
			it.Published.Format("01-02 15:04"), it.Kind, it.SourceName, it.Title)
	}
	return bw.Flush()
}
