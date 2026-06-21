# 07 — Sync Engine & Background Refresh

## Goal

Replace manual/`dump` fetching with a background sync engine: on startup it
fetches all providers concurrently (bounded), upserts results, and pushes them
into the running TUI; on a ticker it re-syncs periodically using each source's
`LastSync`. One failing source never blocks the others or crashes the app, and
its error surfaces in the status line.

## Prerequisites

- Steps 02 (store), 03 (RSS provider + registry), 06 (Gmail provider), 04 (TUI).

## Deliverables

```
internal/syncengine/engine.go
internal/syncengine/engine_test.go    # mock-Provider tests
internal/ui/messages.go               # syncBatchMsg + waitForActivity command
internal/ui/app.go                    # engine wiring, status line, spinner
cmd/renomail/main.go                  # construct engine, pass event channel to UI
```

```bash
go get golang.org/x/sync/errgroup@latest
```

## Design detail

### Engine (`syncengine/engine.go`)

The engine owns the provider set and a results channel. It does **not** import the
UI; it emits plain results that the UI adapts into `tea.Msg`s. `DESIGN.md` §9.

```go
// Result is one provider's fetch outcome for one round.
type Result struct {
    SourceID   string
    SourceName string
    Items      []model.Item
    Err        error
}

type Engine struct {
    providers []source.Provider
    store     *store.Store
    interval  time.Duration
    maxConc   int            // bounded fan-out, e.g. 8
    out       chan Result
}

func New(providers []source.Provider, st *store.Store, interval time.Duration) *Engine {
    return &Engine{providers: providers, store: st, interval: interval,
        maxConc: 8, out: make(chan Result, len(providers))}
}

func (e *Engine) Events() <-chan Result { return e.out }
```

#### Run: initial sweep + periodic ticker

```go
func (e *Engine) Run(ctx context.Context) {
    e.syncAll(ctx)                       // immediate first sweep
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

// syncAll fetches every provider concurrently (bounded), upserts good results,
// persists per-source state, and emits one Result per provider.
func (e *Engine) syncAll(ctx context.Context) {
    sem := make(chan struct{}, e.maxConc)
    var wg sync.WaitGroup
    for _, p := range e.providers {
        wg.Add(1)
        go func(p source.Provider) {
            defer wg.Done()
            sem <- struct{}{}; defer func() { <-sem }()

            since := e.sinceFor(ctx, p.ID())
            items, err := p.Fetch(ctx, since)
            if err == nil && len(items) > 0 {
                if uerr := e.store.UpsertItems(ctx, items); uerr != nil { err = uerr }
            }
            e.persistSourceState(ctx, p)  // LastSync + RSS validators
            // never let one provider's failure escape:
            select {
            case e.out <- Result{SourceID: p.ID(), SourceName: p.Name(), Items: items, Err: err}:
            case <-ctx.Done():
            }
        }(p)
    }
    wg.Wait()
}
```

> `sinceFor` reads the source's stored `LastSync`; `persistSourceState` writes
> `LastSync = now` and, for RSS providers, the updated `ETag`/`LastModified`
> (type-assert to an interface exposing `SourceState()`).

### UI integration (`messages.go`, `app.go`)

The recurring **"wait for activity"** pattern (DESIGN.md §6.3): a command reads
one `Result` from the engine channel, converts it to a `tea.Msg`, and re-arms
itself in `Update` so the stream is drained continuously without a global program
reference.

```go
type syncBatchMsg struct{ res syncengine.Result }

func waitForActivity(ch <-chan syncengine.Result) tea.Cmd {
    return func() tea.Msg {
        res, ok := <-ch
        if !ok { return nil } // channel closed on shutdown
        return syncBatchMsg{res}
    }
}
```

```go
// In app.New / Init:
//   start the engine goroutine and arm the listener + spinner tick.
func (m Model) Init() tea.Cmd {
    return tea.Batch(
        loadItemsCmd(m.store, m.filter), // instant cached view
        waitForActivity(m.events),       // begin draining sync results
        m.spinner.Tick,
    )
}

case syncBatchMsg:
    r := msg.res
    if r.Err != nil {
        m.status = fmt.Sprintf("sync %s: %v", r.SourceName, r.Err) // surface, don't crash
    } else if len(r.Items) > 0 {
        // items already upserted by the engine; refresh the visible feed under the active filter
        return m, tea.Batch(loadItemsCmd(m.store, m.filter), waitForActivity(m.events))
    }
    return m, waitForActivity(m.events) // re-arm regardless
```

Add a `spinner.Model` shown while a sweep is in flight and a `lastSync time.Time`
rendered in the status bar (e.g. `synced 12s ago · 2 sources`).

### Main wiring (`cmd/renomail/main.go`)

```go
func runTUI(cfg config.Config, paths config.Paths) error {
    st, err := store.Open(paths.DBFile); if err != nil { return err }
    defer st.Close()

    ctx, cancel := context.WithCancel(context.Background()); defer cancel()
    rss, err := feeds.BuildRSSProviders(ctx, cfg, st, http.DefaultClient); if err != nil { return err }
    gmailP, warns := feeds.BuildGmailProviders(ctx, cfg, paths)
    providers := append(toIface(rss), toIface(gmailP)...)

    interval, _ := cfg.SyncEvery()
    eng := syncengine.New(providers, st, interval)
    go eng.Run(ctx)

    m, err := ui.New(st, eng.Events(), warns); if err != nil { return err }
    _, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
    return err
}
```

## Implementation flow

1. Add `errgroup` (or use the `sem + WaitGroup` shown).
2. Implement `engine.go` (`Result`, `Engine`, `Run`, `syncAll`, `sinceFor`,
   `persistSourceState`).
3. Add `syncBatchMsg` + `waitForActivity` to `messages.go`.
4. Wire `app.go`: accept the events channel, drain via `waitForActivity`, handle
   `syncBatchMsg` (error→status, items→re-query), add spinner + last-sync.
5. Update `main.go` to build providers, start the engine, pass the channel.
6. Tests + `gofmt`/`vet`/`build`/`test`.

## Validation criteria

- `go build ./...`, `go vet`, `gofmt` clean.
- **Engine tests with mock `Provider`s (temp store):**
  - Multiple mock providers → all their items land in the store after one
    `syncAll`; one `Result` per provider is emitted.
  - A provider returning an error emits a `Result` with `Err` set **and does not
    prevent** the others' items from being upserted (the resilience guarantee).
  - `since` passed to each provider equals its stored `LastSync`; after a sweep,
    `LastSync` advances.
  - Concurrency is bounded (≤ `maxConc` in flight) — assert via a mock that
    records max concurrent calls.
  - `Run` stops cleanly on `ctx` cancel and closes the channel.
- **UI test:** feeding a `syncBatchMsg` with items triggers a re-query; one with
  an error sets the status string and does not panic; the listener re-arms.
- **Manual smoke:** launch, watch the spinner during the first sweep, see RSS +
  Gmail interleave; leave it running past one interval and confirm periodic
  refresh; point one feed at a bad URL and confirm only its error shows.

## Done checklist

- [ ] Concurrent bounded initial sweep + periodic ticker re-sync.
- [ ] Results upserted + per-source state persisted; UI re-queries on new items.
- [ ] One failing source degrades gracefully (status line, others unaffected).
- [ ] Spinner + last-sync indicator wired.
- [ ] Mock-provider + UI tests pass; manual run verified; `gofmt`/`vet`/`test`
      green.
