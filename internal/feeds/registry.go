// Package feeds builds the set of source Providers from the user's config,
// hydrating each provider with the persisted sync state from the store. It is
// shared by the `dump` command (step 03) and the sync engine (step 07).
package feeds

import (
	"context"
	"fmt"
	"net/http"

	"github.com/srhoton/renomail/internal/config"
	"github.com/srhoton/renomail/internal/source/rss"
	"github.com/srhoton/renomail/internal/store"
)

// Provider aliases the RSS provider so callers can depend on the feeds package
// without importing the concrete source package directly.
type Provider = rss.Provider

// BuildRSSProviders expands the OPML files and one-off feeds in cfg into RSS
// providers. Each provider is hydrated with the stored ETag/Last-Modified/
// LastSync for its source so the next fetch can issue a conditional GET. Feeds
// referenced more than once (across OPML files or config) collapse to a single
// provider. A nil client is replaced with one carrying a sane timeout.
func BuildRSSProviders(ctx context.Context, cfg config.Config, st *store.Store, hc *http.Client) ([]*rss.Provider, error) {
	if hc == nil {
		hc = &http.Client{Timeout: rss.DefaultTimeout}
	}

	refs, err := collectRefs(cfg)
	if err != nil {
		return nil, err
	}

	providers := make([]*rss.Provider, 0, len(refs))
	for _, ref := range refs {
		src := ref.Source
		if stored, ok, err := st.GetSource(ctx, src.ID); err != nil {
			return nil, fmt.Errorf("load source %s: %w", src.ID, err)
		} else if ok {
			// Keep the fresh name from config/OPML, but restore the persisted
			// validators and sync bookkeeping.
			src.ETag = stored.ETag
			src.LastModified = stored.LastModified
			src.LastSync = stored.LastSync
		}
		providers = append(providers, rss.New(src, ref.URL, hc))
	}
	return providers, nil
}

// collectRefs gathers feed references from every OPML file and one-off feed in
// cfg, de-duplicated by source ID (first occurrence wins).
func collectRefs(cfg config.Config) ([]rss.FeedRef, error) {
	seen := make(map[string]struct{})
	var refs []rss.FeedRef

	add := func(ref rss.FeedRef) {
		if _, dup := seen[ref.Source.ID]; dup {
			return
		}
		seen[ref.Source.ID] = struct{}{}
		refs = append(refs, ref)
	}

	for _, o := range cfg.OPML {
		path, err := config.ExpandTilde(o.Path)
		if err != nil {
			return nil, err
		}
		imported, err := rss.ImportOPML(path)
		if err != nil {
			return nil, err
		}
		for _, ref := range imported {
			add(ref)
		}
	}

	for _, f := range cfg.Feed {
		add(rss.NewFeedRef(f.URL, f.Title))
	}

	return refs, nil
}
