// Package syncengine runs the background, bounded-concurrent, periodic fetch
// across every provider and fans the results out to the UI. It owns the provider
// set and a results channel; it deliberately does not import the UI, emitting
// plain Result values that the UI adapts into its own messages (DESIGN.md §9). A
// failing provider degrades gracefully — its error rides in its own Result and
// never blocks the others or aborts the sweep.
package syncengine

import (
	"context"
	"fmt"
	"strings"
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
// (already upserted by the engine); Inserted is how many genuinely-new items this
// sweep produced for the source (deduped; re-seen updates excluded), which the UI
// uses to notify on fresh arrivals; Err is non-nil when the fetch or upsert failed,
// in which case Items is best-effort and usually empty (and Inserted is 0).
type Result struct {
	SourceID   string
	SourceName string
	Items      []model.Item
	Inserted   int
	Err        error
}

// DigestFunc posts a single notification summarizing one sweep's new items (e.g.
// the Slack webhook digest). It is injected via SetDigestNotifier; a nil digest
// disables it. The engine calls it once per steady-state sweep — never on the
// initial sweep, which would dump a first run's entire backfill — after the sweep's
// fetches complete, with every source's new items coalesced into one slice. It runs
// in its own goroutine (tracked by digestWG) so a slow webhook never stalls the
// engine's tick/trigger loop; a returned error is surfaced to the UI (as a Result
// with Err) rather than crashing the sweep.
type DigestFunc func(ctx context.Context, newItems []model.Item) error

// digestTimeout bounds a single digest post so a hung webhook cannot run forever.
const digestTimeout = 15 * time.Second

// AlertFunc posts a single threshold notification (e.g. the macOS Notification
// Center banner) carrying a prebuilt message. It is injected via
// SetThresholdNotifier; a nil alert disables it. The engine evaluates the unread
// thresholds at the end of each steady-state sweep — never on the initial backfill
// sweep — and calls AlertFunc only when a kind newly crosses its threshold. It runs
// in its own goroutine (tracked by digestWG) so a slow notifier never stalls the
// engine's tick/trigger loop; a returned error is surfaced to the UI (as a Result
// with Err) rather than crashing the sweep.
type AlertFunc func(ctx context.Context, msg string) error

// alertTimeout bounds a single threshold-alert post so a hung osascript (e.g. a
// Notification permission prompt) cannot run forever.
const alertTimeout = 10 * time.Second

// Default unread thresholds above which a macOS banner fires. "More than 20 unread
// RSS items or more than 2 unread emails" — strict greater-than.
const (
	defaultUnreadEmailThreshold = 2
	defaultUnreadRSSThreshold   = 20
)

// Engine fetches every provider on an interval and emits one Result per provider
// per sweep on its events channel.
type Engine struct {
	providers []source.Provider
	store     *store.Store
	interval  time.Duration
	maxConc   int
	out       chan Result
	trigger   chan struct{}
	digest    DigestFunc     // nil unless a per-sweep digest notifier is installed
	digestWG  sync.WaitGroup // tracks in-flight digest and threshold-alert posts so Run drains them before closing out

	// Threshold-alert state. alert is nil unless a notifier is installed; the
	// thresholds default from SetThresholdNotifier. The latches are edge-trigger
	// guards: a kind fires once when it crosses its threshold and re-arms only after
	// the count drops back to/under it. They are read and written solely on the Run
	// goroutine (in syncAll), so they need no synchronization.
	alert        AlertFunc
	emailThresh  int
	rssThresh    int
	emailLatched bool
	rssLatched   bool
}

// New builds an Engine over the provider set, store, and re-sync interval. The
// events channel is buffered to the provider count so a full sweep can complete
// without blocking on a slow consumer. The trigger channel is buffered to one so
// a manual refresh (Trigger) coalesces rather than blocking the caller.
func New(providers []source.Provider, st *store.Store, interval time.Duration) *Engine {
	buf := max(len(providers), 1)
	return &Engine{
		providers: providers,
		store:     st,
		interval:  interval,
		maxConc:   defaultMaxConc,
		out:       make(chan Result, buf),
		trigger:   make(chan struct{}, 1),
	}
}

// Trigger requests an out-of-band sweep (the UI's force-sync key) without waiting
// for the next tick. It never blocks: if a sweep is already queued the request is
// coalesced into the pending one, so rapid presses cannot pile up.
func (e *Engine) Trigger() {
	select {
	case e.trigger <- struct{}{}:
	default:
	}
}

// Events returns the receive end of the results channel. It is closed when Run
// returns (on context cancellation), so a ranging consumer terminates cleanly.
func (e *Engine) Events() <-chan Result { return e.out }

// SetDigestNotifier installs the per-sweep digest notifier (e.g. the Slack webhook
// digest). It must be called before Run starts the engine goroutine; passing nil
// leaves the digest disabled. See DigestFunc for the calling contract.
func (e *Engine) SetDigestNotifier(fn DigestFunc) { e.digest = fn }

// SetThresholdNotifier installs the unread-threshold notifier (e.g. the macOS
// Notification Center banner) and defaults any unset thresholds. It must be called
// before Run starts the engine goroutine; passing nil leaves threshold alerts
// disabled. See AlertFunc for the calling contract.
func (e *Engine) SetThresholdNotifier(fn AlertFunc) {
	e.alert = fn
	if e.emailThresh <= 0 {
		e.emailThresh = defaultUnreadEmailThreshold
	}
	if e.rssThresh <= 0 {
		e.rssThresh = defaultUnreadRSSThreshold
	}
}

// Run performs an immediate first sweep, then re-syncs on the interval — or
// whenever Trigger requests an out-of-band sweep — until the context is cancelled,
// at which point it waits for any in-flight digest post to finish, then closes the
// events channel and returns. Draining digestWG before the close is what makes the
// asynchronous digest send-safe: no digest goroutine can send on out after it closes.
func (e *Engine) Run(ctx context.Context) {
	e.syncAll(ctx, true) // initial sweep: backfill, never digested
	t := time.NewTicker(e.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			e.digestWG.Wait()
			close(e.out)
			return
		case <-t.C:
			e.syncAll(ctx, false)
		case <-e.trigger:
			e.syncAll(ctx, false)
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
//
// When a digest notifier is installed and this is not the initial sweep, every
// provider's new items are collected and, after the sweep completes, posted as a
// single coalesced digest (the per-source Results still stream to the UI as before).
func (e *Engine) syncAll(ctx context.Context, initial bool) {
	now := time.Now()
	digesting := e.digest != nil && !initial

	var (
		g      errgroup.Group
		mu     sync.Mutex
		states []model.Source // successful providers' refreshed state, persisted in one batch
		newAll []model.Item   // every provider's new items, coalesced for one digest post
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
			var newItems []model.Item
			if len(items) > 0 {
				n, uerr := e.store.UpsertItems(ctx, items)
				if uerr != nil && err == nil {
					err = uerr
				}
				newItems = n
			}
			if err == nil {
				st := source.StateOf(p, now)
				mu.Lock()
				states = append(states, st)
				mu.Unlock()
			}
			if digesting && len(newItems) > 0 {
				mu.Lock()
				newAll = append(newAll, newItems...)
				mu.Unlock()
			}
			select {
			case e.out <- Result{SourceID: p.ID(), SourceName: p.Name(), Items: items, Inserted: len(newItems), Err: err}:
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

	if digesting && len(newAll) > 0 {
		// Post asynchronously so a slow webhook never delays the next tick/trigger.
		// digestWG lets Run wait for the post before closing out (see Run).
		e.digestWG.Add(1)
		go func() {
			defer e.digestWG.Done()
			e.postDigest(ctx, newAll)
		}()
	}

	e.maybeAlert(ctx, initial)
}

// maybeAlert evaluates the unread-count thresholds after a sweep and fires the
// installed notifier once when a kind crosses its threshold. The initial backfill
// sweep is skipped so a first run does not banner mid-load; a standing backlog is
// then announced on the first steady-state sweep of the session. Each kind latches
// independently — it fires on the below→over transition and re-arms only after the
// count falls back to/under the threshold — so a persistent backlog does not re-nag
// every sweep. Counting is best-effort: a query error is swallowed (the next sweep
// retries) rather than crashing the engine.
func (e *Engine) maybeAlert(ctx context.Context, initial bool) {
	if e.alert == nil || initial {
		return
	}
	emailUnread, err := e.store.Count(ctx, model.Filter{
		Kinds: map[model.Kind]bool{model.KindEmail: true}, Read: model.ReadUnreadOnly,
	})
	if err != nil {
		return
	}
	rssUnread, err := e.store.Count(ctx, model.Filter{
		Kinds: map[model.Kind]bool{model.KindRSS: true}, Read: model.ReadUnreadOnly,
	})
	if err != nil {
		return
	}

	emailOver := emailUnread > e.emailThresh
	rssOver := rssUnread > e.rssThresh
	var parts []string
	if emailOver && !e.emailLatched {
		parts = append(parts, fmt.Sprintf("%d unread emails", emailUnread))
	}
	if rssOver && !e.rssLatched {
		parts = append(parts, fmt.Sprintf("%d unread RSS items", rssUnread))
	}
	e.emailLatched = emailOver
	e.rssLatched = rssOver
	if len(parts) == 0 {
		return
	}

	// The notifier supplies the "renomail" title, so the message is just the body.
	msg := strings.Join(parts, ", ")
	// Post asynchronously (bounded by alertTimeout) so a hung osascript never delays
	// the next tick/trigger. digestWG lets Run wait for the post before closing out.
	e.digestWG.Add(1)
	go func() {
		defer e.digestWG.Done()
		e.postAlert(ctx, msg)
	}()
}

// postAlert delivers a prebuilt threshold message to the injected notifier under a
// bounded timeout. A failure is surfaced to the UI as a Result with Err (rendered on
// the status line) rather than crashing the sweep; if the consumer is already gone
// the error is dropped. It is a no-op once the context is cancelled, so a shutdown
// does not emit spurious "context canceled" alert errors.
func (e *Engine) postAlert(ctx context.Context, msg string) {
	if ctx.Err() != nil {
		return
	}
	actx, cancel := context.WithTimeout(ctx, alertTimeout)
	defer cancel()
	if err := e.alert(actx, msg); err != nil {
		if ctx.Err() != nil {
			return // cancelled mid-post: shutdown noise, not a real delivery failure
		}
		select {
		case e.out <- Result{SourceName: "macOS", Err: err}:
		case <-ctx.Done():
		}
	}
}

// postDigest delivers the sweep's coalesced new items to the injected digest
// notifier under a bounded timeout. A failure is surfaced to the UI as a Result
// with Err (rendered on the status line) rather than crashing the sweep; if the
// consumer is already gone the error is dropped. It is a no-op once the context is
// cancelled, so a shutdown does not emit spurious "context canceled" digest errors.
func (e *Engine) postDigest(ctx context.Context, items []model.Item) {
	if ctx.Err() != nil {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, digestTimeout)
	defer cancel()
	if err := e.digest(dctx, items); err != nil {
		if ctx.Err() != nil {
			return // cancelled mid-post: shutdown noise, not a real delivery failure
		}
		select {
		case e.out <- Result{SourceName: "Slack", Err: err}:
		case <-ctx.Done():
		}
	}
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
