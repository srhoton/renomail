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
	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/source"
	"github.com/srhoton/renomail/internal/source/rss"
	"github.com/srhoton/renomail/internal/store"
)

// runDump executes the full pipeline once — build every provider (RSS + Gmail),
// fetch each, upsert to the store, then print the merged feed — to prove the data
// path end to end. It opens the store and builds the providers, delegating the
// loop to dumpFeeds so the core is unit-testable.
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
	providers, warns, err := buildProviders(ctx, cfg, paths, st, client)
	if err != nil {
		return err
	}
	for _, warn := range warns {
		fmt.Fprintf(os.Stderr, "warn: %v\n", warn)
	}
	return dumpFeeds(ctx, w, os.Stderr, st, providers)
}

// dumpFeeds fetches each provider, upserts its items and refreshed source state,
// then prints every stored item newest-first to out. Diagnostic lines (a skipped
// provider, or a feed unchanged since the last fetch) go to errOut. A fetch
// failure for one provider is reported and skipped rather than aborting the run.
func dumpFeeds(ctx context.Context, out, errOut io.Writer, st *store.Store, providers []source.Provider) error {
	now := time.Now()
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
		if _, err := st.UpsertItems(ctx, items); err != nil {
			return fmt.Errorf("upsert %s: %w", p.Name(), err)
		}
		if err := st.UpsertSource(ctx, source.StateOf(p, now)); err != nil {
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
