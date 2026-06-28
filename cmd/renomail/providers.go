package main

import (
	"context"
	"net/http"

	"github.com/srhoton/renomail/internal/config"
	"github.com/srhoton/renomail/internal/feeds"
	"github.com/srhoton/renomail/internal/source"
	"github.com/srhoton/renomail/internal/store"
)

// buildProviders constructs the full provider set — RSS feeds, Gmail accounts, and
// (on macOS, when enabled) local Apple Mail accounts — as a single []source.Provider,
// the uniform slice the dump command and the sync engine both consume. Gmail accounts
// that are not yet authorized and Apple Mail (missing Full Disk Access / not macOS)
// are skipped and returned as advisory warnings rather than failing the run; an RSS
// build error (malformed OPML, unreadable config) is fatal and returned as err.
func buildProviders(
	ctx context.Context,
	cfg config.Config,
	paths config.Paths,
	st *store.Store,
	client *http.Client,
) (providers []source.Provider, warns []error, err error) {
	rssProviders, err := feeds.BuildRSSProviders(ctx, cfg, st, client)
	if err != nil {
		return nil, nil, err
	}
	gmailProviders, warns := feeds.BuildGmailProviders(ctx, cfg, paths)
	appleProviders, appleWarns := feeds.BuildAppleMailProviders(ctx, cfg)
	warns = append(warns, appleWarns...)

	providers = make([]source.Provider, 0, len(rssProviders)+len(gmailProviders)+len(appleProviders))
	for _, p := range rssProviders {
		providers = append(providers, p)
	}
	for _, p := range gmailProviders {
		providers = append(providers, p)
	}
	for _, p := range appleProviders {
		providers = append(providers, p)
	}
	return providers, warns, nil
}
