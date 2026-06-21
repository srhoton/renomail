# 03 — RSS Provider & OPML Import

## Goal

Implement the first `Provider`: RSS/Atom feeds sourced from OPML files (and
one-off feeds in config). Add a `renomail dump` debug subcommand that runs the
full path — config → import feeds → fetch → upsert → query → print — proving the
data pipeline end-to-end **before any TUI exists** and **without authentication**.

## Prerequisites

- Step 02 (model, store, `Provider` interface).

## Deliverables

```
internal/source/rss/rss.go        # Provider impl + entry→Item mapping
internal/source/rss/opml.go       # OPML import → []model.Source
internal/source/rss/rss_test.go
internal/source/rss/opml_test.go
internal/feeds/registry.go        # build providers from config (shared w/ 07)
cmd/renomail/dump.go              # `renomail dump` subcommand
testdata/sample_rss.xml
testdata/sample_atom.xml
testdata/sample.opml
```

```bash
go get github.com/mmcdole/gofeed@latest
go get github.com/gilliek/go-opml@latest
```

## Design detail

### OPML import (`opml.go`)

Parse one OPML file into `model.Source`s. Each `<outline>` with an `xmlUrl`
becomes a feed source; the source ID is a hash of the feed URL so it is stable.

```go
// ImportOPML reads an OPML file and returns one Source per feed outline.
func ImportOPML(path string) ([]model.Source, error) {
    doc, err := opml.NewOPMLFromFile(path)
    if err != nil { return nil, fmt.Errorf("read opml %s: %w", path, err) }
    var out []model.Source
    var walk func(os []opml.Outline)
    walk = func(os []opml.Outline) {
        for _, o := range os {
            if o.XMLURL != "" {
                out = append(out, model.Source{
                    ID:   feedID(o.XMLURL),
                    Name: firstNonEmpty(o.Title, o.Text, o.XMLURL),
                    Kind: model.KindRSS,
                })
            }
            walk(o.Outlines) // OPML nests categories
        }
    }
    walk(doc.Body.Outlines)
    return out, nil
}

func feedID(url string) string { return "rss:" + model.StableID("rss", url) }
```

### Provider (`rss.go`)

One `Provider` per feed. It carries the feed URL and the conditional-GET
validators loaded from the store, and writes updated validators back via the
returned `Source` state (the sync engine persists them).

```go
type Provider struct {
    src    model.Source // ID, Name, Kind, ETag, LastModified
    url    string
    client *http.Client
    parser *gofeed.Parser
}

func New(src model.Source, url string, client *http.Client) *Provider { /* ... */ }

func (p *Provider) ID() string        { return p.src.ID }
func (p *Provider) Name() string      { return p.src.Name }
func (p *Provider) Kind() model.Kind  { return model.KindRSS }

func (p *Provider) Fetch(ctx context.Context, since time.Time) ([]model.Item, error) {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
    if p.src.ETag != ""         { req.Header.Set("If-None-Match", p.src.ETag) }
    if p.src.LastModified != "" { req.Header.Set("If-Modified-Since", p.src.LastModified) }

    resp, err := p.client.Do(req)
    if err != nil { return nil, fmt.Errorf("fetch %s: %w", p.url, err) }
    defer resp.Body.Close()
    if resp.StatusCode == http.StatusNotModified { return nil, nil } // polite no-op
    if resp.StatusCode != http.StatusOK { return nil, fmt.Errorf("fetch %s: status %d", p.url, resp.StatusCode) }

    // capture validators for next time
    p.src.ETag = resp.Header.Get("ETag")
    p.src.LastModified = resp.Header.Get("Last-Modified")

    feed, err := p.parser.Parse(resp.Body)
    if err != nil { return nil, fmt.Errorf("parse %s: %w", p.url, err) }

    var items []model.Item
    for _, e := range feed.Items {
        items = append(items, p.toItem(feed, e))
    }
    return items, nil
}

// RSS bodies arrive with the list payload, so Body is usually a no-op.
func (p *Provider) Body(ctx context.Context, item *model.Item) error { return nil }

// SourceState exposes updated ETag/LastModified/LastSync for the engine to persist.
func (p *Provider) SourceState() model.Source { return p.src }
```

#### Entry → Item mapping

```go
func (p *Provider) toItem(feed *gofeed.Feed, e *gofeed.Item) model.Item {
    native := firstNonEmpty(e.GUID, e.Link)
    body := firstNonEmpty(e.Content, e.Description)
    published := time.Now()
    if e.PublishedParsed != nil { published = *e.PublishedParsed } else if e.UpdatedParsed != nil { published = *e.UpdatedParsed }
    author := ""
    if e.Author != nil { author = e.Author.Name }
    return model.Item{
        ID:         model.StableID(p.src.ID, native),
        Kind:       model.KindRSS,
        SourceID:   p.src.ID,
        SourceName: firstNonEmpty(p.src.Name, feed.Title),
        Author:     firstNonEmpty(author, feed.Title),
        Title:      e.Title,
        Snippet:    snippet(body, 200),  // strip tags, truncate
        URL:        e.Link,
        Published:  published,
        Fetched:    time.Now(),
        BodyHTML:   body,
        BodyText:   stripTags(body),
    }
}
```

### Provider registry (`internal/feeds/registry.go`)

Shared by the `dump` command (this step) and the sync engine (07). Turns config
into a set of `Provider`s, hydrating each RSS provider's validators from the
store.

```go
// BuildRSSProviders expands OPML files + one-off feeds from config into
// providers, restoring stored ETag/LastModified/Name from the store.
func BuildRSSProviders(ctx context.Context, cfg config.Config, st *store.Store, hc *http.Client) ([]*rss.Provider, error)
```

### `dump` subcommand (`cmd/renomail/dump.go`)

```go
// Usage: renomail dump
// Imports feeds, fetches each, upserts to the store, then prints the merged feed.
func runDump(ctx context.Context, cfg config.Config, paths config.Paths) error {
    st, err := store.Open(paths.DBFile); if err != nil { return err }
    defer st.Close()
    providers, err := feeds.BuildRSSProviders(ctx, cfg, st, http.DefaultClient)
    if err != nil { return err }
    for _, p := range providers {
        items, err := p.Fetch(ctx, time.Time{})
        if err != nil { fmt.Fprintf(os.Stderr, "warn: %s: %v\n", p.Name(), err); continue }
        if err := st.UpsertItems(ctx, items); err != nil { return err }
        src := p.SourceState(); src.LastSync = time.Now()
        _ = st.UpsertSource(ctx, src)
    }
    all, err := st.Query(ctx, model.Filter{}); if err != nil { return err }
    for _, it := range all {
        fmt.Printf("%s  [%s] %-24s  %s\n", it.Published.Format("01-02 15:04"), it.Kind, it.SourceName, it.Title)
    }
    return nil
}
```

Wire `main.go` to dispatch `dump` (e.g. `os.Args[1] == "dump"`); default (no
subcommand) still prints the config summary until step 04 launches the TUI.

## Implementation flow

1. Add `gofeed`, `go-opml`; add `testdata/` fixtures (a small RSS, an Atom, an
   OPML referencing local/remote feeds).
2. Implement `opml.go` (`ImportOPML`, `feedID`).
3. Implement `rss.go` (`Provider`, `Fetch`, `toItem`, `SourceState`, helpers:
   `snippet`, `stripTags`, `firstNonEmpty`).
4. Implement `feeds/registry.go` (`BuildRSSProviders`).
5. Implement `cmd/renomail/dump.go` and wire dispatch in `main.go`.
6. Tests + `gofmt`/`vet`/`build`/`test`.

## Validation criteria

- `go build ./...`, `go vet`, `gofmt` clean.
- **Parser tests (fixtures, no network):**
  - `gofeed` parse of `sample_rss.xml` and `sample_atom.xml` → expected counts;
    `toItem` produces correct `Title`, `URL`, non-empty `BodyHTML`, sane
    `Published`, and a deterministic `ID`.
  - `Snippet`/`stripTags` remove markup and truncate.
  - `ImportOPML(sample.opml)` returns the expected sources, **including nested
    outlines**, each with a stable `feedID`.
  - A `304 Not Modified` response yields `(nil, nil)` (use an `httptest.Server`).
- **Manual smoke:** create `~/.config/renomail/config.toml` pointing at a real
  OPML, run `renomail dump`, confirm items print newest-first; run again and
  confirm conditional GET avoids re-parsing unchanged feeds (add a debug log).

## Done checklist

- [ ] OPML import handles nested outlines; RSS provider fetches + maps entries.
- [ ] Conditional GET (ETag/Last-Modified, 304 handling) implemented.
- [ ] `feeds.BuildRSSProviders` builds providers from config + store state.
- [ ] `renomail dump` prints a merged, persisted feed end-to-end.
- [ ] Fixture tests + manual run green; `gofmt`/`vet`/`test` clean.
