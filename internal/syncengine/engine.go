// Package syncengine runs the background, bounded-concurrent, periodic fetch
// across every provider and fans the results out to the UI. It owns the provider
// set and a results channel; it deliberately does not import the UI, emitting
// plain Result values that the UI adapts into its own messages (DESIGN.md §9). A
// failing provider degrades gracefully — its error rides in its own Result and
// never blocks the others or aborts the sweep.
package syncengine

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/source"
	"github.com/srhoton/renomail/internal/store"
)

// defaultMaxConc bounds the number of providers fetched concurrently in one
// sweep, capping outbound load (and, transitively, Gmail's per-message fan-out)
// regardless of how many sources are configured.
const defaultMaxConc = 8

// Result is one provider's outcome for one sweep. Items is what Fetch returned
// (already upserted by the engine); Err is non-nil when the fetch or upsert
// failed, in which case Items is best-effort and usually empty.
type Result struct {
	SourceID   string
	SourceName string
	Items      []model.Item
	Err        error
}

// Engine fetches every provider on an interval and emits one Result per provider
// per sweep on its events channel.
type Engine struct {
	providers []source.Provider
	store     *store.Store
	interval  time.Duration
	maxConc   int
	out       chan Result
}

// New builds an Engine over the provider set, store, and re-sync interval. The
// events channel is buffered to the provider count so a full sweep can complete
// without blocking on a slow consumer.
func New(providers []source.Provider, st *store.Store, interval time.Duration) *Engine {
	buf := max(len(providers), 1)
	return &Engine{
		providers: providers,
		store:     st,
		interval:  interval,
		maxConc:   defaultMaxConc,
		out:       make(chan Result, buf),
	}
}

// Events returns the receive end of the results channel. It is closed when Run
// returns (on context cancellation), so a ranging consumer terminates cleanly.
func (e *Engine) Events() <-chan Result { return e.out }

// Run performs an immediate first sweep, then re-syncs on the interval until the
// context is cancelled, at which point it closes the events channel and returns.
func (e *Engine) Run(ctx context.Context) {
	e.syncAll(ctx)
	t := time.NewTicker(e.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			close(e.out)
			return
		case <-t.C:
			e.syncAll(ctx)
		}
	}
}

// syncAll fetches every provider concurrently (bounded by maxConc), upserts each
// provider's items (including any partial results harvested before an error),
// advances per-source state only for providers that fully succeeded, and emits one
// Result per provider. A provider's failure is captured in its Result rather than
// propagated, so one bad source never blocks or fails the others.
//
// LastSync is the sweep's high-water mark, captured once before any fetch begins —
// not after a fetch completes — so a message that arrives mid-sweep is not stranded
// between the listing snapshot and a later persist time. Crucially, a provider's
// state is advanced only when its fetch+upsert succeeded; on failure its LastSync
// is left untouched so the next sweep retries the same window rather than silently
// skipping the messages it could not fetch.
func (e *Engine) syncAll(ctx context.Context) {
	now := time.Now()

	var (
		g      errgroup.Group
		mu     sync.Mutex
		states []model.Source // successful providers' refreshed state, persisted in one batch
	)
	g.SetLimit(e.maxConc)
	for _, p := range e.providers {
		g.Go(func() error {
			since := e.sinceFor(ctx, p.ID())
			items, err := p.Fetch(ctx, since)
			// Upsert whatever we got. A provider may return partial results
			// alongside an error (e.g. Gmail harvesting the messages it did fetch
			// before one Get failed); those items are still valid and must not be
			// dropped.
			if len(items) > 0 {
				if uerr := e.store.UpsertItems(ctx, items); uerr != nil && err == nil {
					err = uerr
				}
			}
			if err == nil {
				st := source.StateOf(p, now)
				mu.Lock()
				states = append(states, st)
				mu.Unlock()
			}
			select {
			case e.out <- Result{SourceID: p.ID(), SourceName: p.Name(), Items: items, Err: err}:
			case <-ctx.Done():
				// The consumer is going away; deliberately discard this Result
				// rather than block. Run will close the channel next.
			}
			return nil
		})
	}
	_ = g.Wait()

	// Persist all successful sources in a single transaction (best-effort: a state
	// write failure must not crash the sweep, so the error is intentionally dropped).
	_ = e.store.UpsertSources(ctx, states)
}

// sinceFor reads the source's stored LastSync, the lower bound for an incremental
// fetch. A source with no stored state (first run, or unreadable) yields the zero
// time, which providers treat as a cold start.
func (e *Engine) sinceFor(ctx context.Context, id string) time.Time {
	src, ok, err := e.store.GetSource(ctx, id)
	if err != nil || !ok {
		return time.Time{}
	}
	return src.LastSync
}
