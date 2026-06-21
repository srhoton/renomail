# renomail — Design Document & Build Blueprint

## Context

`renomail` is a new, greenfield personal TUI application. The goal is a single
terminal app that presents a **unified, continuously-updating feed** blending
**Gmail messages** (from multiple accounts) and **RSS/Atom items** (from feeds
imported via OPML), all in one scrollable list. The user can filter the feed,
mark items read/unread, and open an item to read its full content.

This is a **reader**, not a full mail/RSS client: we pull and display content
only. Read/unread is tracked **locally** — we never write state back to Gmail or
the feeds. This document is the blueprint for the first version.

### Confirmed decisions

| Area | Decision |
|------|----------|
| Email access | **Gmail API + OAuth2**, `gmail.readonly` scope, per-account browser flow |
| Persistence | **SQLite** caching both items *and* read/unread state (fast start, offline browse) |
| Layout | **Full-screen unified list → drill into a full-screen reader** (Enter opens, Esc returns) |
| Content rendering | **Rich: HTML → markdown → Glamour** styled terminal output |

---

## 1. Technology Stack

| Concern | Library | Notes |
|---------|---------|-------|
| TUI runtime | `github.com/charmbracelet/bubbletea` | Elm architecture; deviate where it helps (see §6) |
| TUI widgets | `github.com/charmbracelet/bubbles` | `list`/custom delegate, `viewport`, `textinput`, `spinner`, `help`, `key` |
| Styling | `github.com/charmbracelet/lipgloss` | Colors for read/unread, layout, status bar |
| Markdown render | `github.com/charmbracelet/glamour` | Renders item bodies in the reader |
| HTML → Markdown | `github.com/JohannesKaufmann/html-to-markdown/v2` | Converts email/RSS HTML before Glamour |
| Gmail | `google.golang.org/api/gmail/v1` + `golang.org/x/oauth2`, `.../oauth2/google` | Read-only; loopback OAuth |
| RSS/Atom | `github.com/mmcdole/gofeed` | Parses RSS, Atom, JSON feeds; conditional GET supported |
| OPML | `github.com/gilliek/go-opml` | Import feed lists |
| Storage | `modernc.org/sqlite` (pure-Go, cgo-free) via `database/sql` | Cross-compiles cleanly; no cgo toolchain |
| Config | `github.com/BurntSushi/toml` | Simple human-editable config |
| Open in browser | `github.com/pkg/browser` | `o` keybinding opens permalinks |
| Token storage (optional) | `github.com/zalando/go-keyring` | Default is `0600` file; keyring is an opt-in upgrade |
| Model tests | `github.com/charmbracelet/x/exp/teatest` | Golden-file view tests + `Update()` unit tests |

Go version: latest stable (1.22+). Module path: `github.com/steverhoton/renomail`
(adjust to the chosen remote). Build system stays plain `go build` / `go test`.

---

## 2. Project Layout (idiomatic Go)

```
renomail/
├── go.mod
├── cmd/renomail/main.go        # entrypoint: load config, open store, start program
├── internal/
│   ├── config/                 # TOML load/save, paths (XDG dirs)
│   ├── model/                  # domain types: Item, Source, Kind, Filter
│   ├── store/                  # SQLite repository (items, sources, meta)
│   ├── source/
│   │   ├── source.go           # Provider interface
│   │   ├── gmail/              # Gmail provider + OAuth flow + MIME parsing
│   │   └── rss/                # gofeed provider + OPML import
│   ├── syncengine/             # background fetch scheduler, fan-in to UI
│   ├── render/                 # HTML → markdown → Glamour pipeline
│   └── ui/
│       ├── app.go              # root bubbletea Model (view router)
│       ├── feed/               # list view + item delegate
│       ├── reader/             # viewport-based detail view
│       ├── filterbar/          # `/` filter input + quick toggles
│       ├── keys/               # key bindings (bubbles/key)
│       └── styles/             # lipgloss styles / theme
└── testdata/                   # sample MIME, RSS XML, OPML fixtures
```

Rationale: organize by feature/domain, keep `cmd` thin, keep all real logic in
`internal/`. Mirrors the Go layout rules in the global guide.

---

## 3. Domain Model (`internal/model`)

```go
type Kind string
const (
    KindEmail Kind = "email"
    KindRSS   Kind = "rss"
)

type ReadState int // for filtering
const (ReadAny ReadState = iota; ReadUnreadOnly; ReadReadOnly)

// Item is the unified feed unit — both an email and an RSS entry map onto it.
type Item struct {
    ID         string    // stable: sha256(sourceID + nativeID)
    Kind       Kind
    SourceID   string    // account id ("me@gmail.com") or feed id (feed URL hash)
    SourceName string    // display name: "Personal Gmail" / "Hacker News"
    Author     string    // From header / feed entry author
    Title      string    // Subject / entry title
    Snippet    string    // short preview for the list row
    URL        string    // permalink (RSS) or Gmail web deep-link (email)
    Published  time.Time // sort key (desc)
    Fetched    time.Time
    Read       bool      // LOCAL state only
    BodyHTML   string    // lazily populated for email; usually present for RSS
    BodyText   string    // fallback / search
}

// Source is a configured origin (one Gmail account or one feed).
type Source struct {
    ID, Name string
    Kind     Kind
    // sync bookkeeping:
    LastSync     time.Time
    ETag         string // RSS conditional GET
    LastModified string // RSS conditional GET
}

// Filter drives both the SQL query and the visible feed.
type Filter struct {
    Kinds     map[Kind]bool // empty = all
    SourceIDs map[string]bool
    Read      ReadState
    Search    string        // matches Title/Author/Snippet/BodyText
}
```

---

## 4. Source Providers (`internal/source`)

A single interface lets the sync engine treat Gmail and RSS uniformly:

```go
type Provider interface {
    ID() string
    Name() string
    Kind() model.Kind
    // Fetch returns items at/after `since` (the source's LastSync), already
    // populated for the list view (headers + snippet). Body may be empty.
    Fetch(ctx context.Context, since time.Time) ([]model.Item, error)
    // Body lazily loads full content for one item (used when reader opens).
    Body(ctx context.Context, item *model.Item) error
}
```

### 4.1 Gmail provider (`source/gmail`)

- **Auth:** OAuth2 *loopback/desktop* flow. App reads an OAuth **client** file
  (`credentials.json`, downloaded once by the user from Google Cloud → APIs &
  Services → Credentials → Desktop app). Per-account flow opens the browser,
  user consents, code is exchanged on a `localhost` redirect. Refresh token
  persisted to `token-<account>.json` (mode `0600`) under the config dir.
- **Scope:** `gmail.readonly` only.
- **List fetch:** `users.messages.list` with a query
  (`in:inbox newer_than:<lookback>`, default 30d), paginated. For each id, a
  metadata `get` (`format=METADATA`, headers `From`,`Subject`,`Date` + the API
  `snippet`) builds the list row cheaply.
- **Body (lazy):** on reader open, `get` with `format=FULL`; walk MIME parts,
  prefer `text/html`, fall back to `text/plain`; base64url-decode bodies.
- **Multiple accounts:** one `Provider` instance per account; account email is
  the `SourceID`.
- **Dedup:** native Gmail message id feeds the stable `Item.ID` hash.

### 4.2 RSS provider (`source/rss`)

- **OPML import:** `go-opml` parses one or more OPML files (path(s) in config)
  into `{url, title}` entries; each becomes a `Source` (id = hash of feed URL).
- **Fetch:** `gofeed.Parser` fetches + parses each feed. Map each entry → `Item`
  (`Content` or `Description` as `BodyHTML`; `Link` as `URL`;
  `PublishedParsed`/`UpdatedParsed` as `Published`).
- **Politeness:** send `If-None-Match`/`If-Modified-Since` from the stored
  `ETag`/`LastModified`; on `304` skip. Bound concurrency (e.g. 8 feeds at once).
- **Body:** typically already present; if a feed only ships summaries, `o` opens
  the permalink in a browser.

---

## 5. Storage (`internal/store`, SQLite)

Pure-Go `modernc.org/sqlite`. DB at `~/.local/share/renomail/renomail.db`
(XDG data dir). Schema (with a `meta.schema_version` for migrations):

```sql
CREATE TABLE items (
  id TEXT PRIMARY KEY, kind TEXT, source_id TEXT, source_name TEXT,
  author TEXT, title TEXT, snippet TEXT, url TEXT,
  published INTEGER, fetched INTEGER,
  read INTEGER DEFAULT 0, body_html TEXT, body_text TEXT
);
CREATE INDEX idx_items_published ON items(published DESC);
CREATE INDEX idx_items_read      ON items(read);
CREATE INDEX idx_items_kind      ON items(kind);
CREATE INDEX idx_items_source    ON items(source_id);

CREATE TABLE sources (
  id TEXT PRIMARY KEY, kind TEXT, name TEXT,
  last_sync INTEGER, etag TEXT, last_modified TEXT
);
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);
```

Repository methods: `UpsertItems`, `Query(Filter) []Item` (builds parameterized
`WHERE`), `SetRead(id, bool)`, `MarkAllRead(Filter)`, `GetBody/SetBody`,
`GetSource/UpsertSource`. Upserts use `ON CONFLICT(id)` and **preserve the local
`read` flag** so re-fetching never resets read state.

---

## 6. TUI Architecture (`internal/ui`)

Bubble Tea (Elm: `Model`/`Update`/`View`). A root `app.Model` routes between
view states. We follow Bubble Tea idioms but deviate freely where it helps
(e.g. background producers pushing via a channel — §6.3).

### 6.1 Root model

```go
type view int
const (viewFeed view = iota; viewReader; viewFilter; viewHelp)

type Model struct {
    view     view
    feed     feed.Model       // list
    reader   reader.Model     // viewport
    filter   model.Filter
    store    *store.Store
    sync     *syncengine.Engine
    events   <-chan tea.Msg   // background sync results
    spinner  spinner.Model
    status   string           // last-sync / per-source error line
    w, h     int
}
```

- **Startup:** load config → open store → **immediately `Query()` cached items
  into the feed** (instant UI) → start sync engine in the background.
- **Routing:** `viewFeed` shows the list; Enter → `viewReader` (lazy-load body
  via a `tea.Cmd`); Esc → back. `/` → `viewFilter`; `?` → `viewHelp`.

### 6.2 Messages & commands

- `itemsLoadedMsg{[]Item}` — initial DB load / re-query after filter change.
- `syncBatchMsg{sourceID, []Item, error}` — a provider finished a fetch round.
- `bodyLoadedMsg{id, html, err}` — lazy body ready → render → set viewport.
- `syncTickMsg` — periodic re-sync trigger (ticker).
- DB queries, body loads, and renders run **inside `tea.Cmd`s** (off the UI
  update goroutine) so the UI never blocks on I/O.

### 6.3 Background sync → UI (deliberate deviation)

The sync engine runs provider fetches on its own goroutines and emits results on
a channel. The UI drains it with the idiomatic **recurring "wait for activity"**
command: `waitForActivity(events)` reads one `tea.Msg` from the channel and, in
`Update`, re-issues itself. This keeps async producers decoupled from the program
without needing a global `*tea.Program` reference.

### 6.4 Feed list view (`ui/feed`)

- Full-screen scroll list. Custom row delegate renders: read dot (`●` unread /
  `○` read), `Kind` tag, `SourceName`, `Title`/snippet, relative age.
- **Read/unread styling:** unread = bright/bold; read = dimmed (lipgloss faint).
- Sorted by `Published` desc; new sync items merge in and re-sort.

### 6.5 Reader view (`ui/reader`)

- `bubbles/viewport` scrolls the rendered body. Header shows From/Author,
  Title, Source, date. Opening an item marks it read (persisted).
- Body produced by the §7 render pipeline. `o` opens `Item.URL` in the browser.

### 6.6 Filtering (`ui/filterbar`)

- `/` opens a `textinput` for free-text search (Title/Author/Snippet/BodyText).
- Quick toggles from the feed: `e` emails only, `r` RSS only, `u` unread only,
  `a` reset to all. Source-scoping selectable from a source list.
- Any change rebuilds `model.Filter`, re-runs `store.Query`, refreshes the list.

### 6.7 Keybindings (`ui/keys`, via `bubbles/key` + `help`)

`j/k`,`↑/↓` move · `g/G` top/bottom · `Enter` open · `Esc` back · `m` toggle
read · `M` mark-all-read (current filter) · `/` search · `e`/`r`/`u`/`a` quick
filters · `o` open in browser · `R` force sync · `?` help · `q` quit.

---

## 7. Render Pipeline (`internal/render`)

`BodyHTML` → `html-to-markdown` (strips scripts/styles, preserves headings,
links, lists, code) → `glamour.Render` with a terminal style (auto light/dark) →
string fed to the reader viewport. If HTML is empty, render `BodyText` directly.
Renderer is width-aware (re-render on terminal resize).

---

## 8. Configuration (`internal/config`)

`~/.config/renomail/config.toml` (XDG config dir). OAuth `credentials.json` and
`token-*.json` live alongside it.

```toml
sync_interval = "5m"
lookback      = "30d"      # Gmail message window

[[gmail]]
account = "me@gmail.com"

[[gmail]]
account = "work@gmail.com"

[[opml]]
path = "~/feeds.opml"      # may list several; or use [[feed]] for one-offs
```

---

## 9. Sync Engine (`internal/syncengine`)

1. On start, build a `Provider` per configured Gmail account + per RSS feed.
2. Run an initial fetch for all providers **concurrently** (bounded worker pool),
   each emitting a `syncBatchMsg` as it returns; the UI upserts to the store and
   merges into the visible feed.
3. A ticker (`sync_interval`) triggers periodic re-syncs using each source's
   `LastSync` as the `since`.
4. Per-provider errors are captured into the batch message and surfaced in the
   status line — **one failing source never crashes the app or blocks others.**

---

## 10. Security & Robustness

- OAuth tokens and `credentials.json` stored `0600`; optional OS keychain via
  `go-keyring`. Read-only Gmail scope — no destructive capability.
- All SQL parameterized (no string interpolation).
- HTML sanitized during markdown conversion (scripts/styles dropped) before
  terminal rendering.
- Context timeouts on every network call; graceful degradation on partial
  failure (per global "no ownership-dodging" + error-handling rules).

---

## 11. Testing Strategy

- **Providers:** table-driven tests against `testdata/` fixtures — sample MIME
  (multipart, html+plain), RSS/Atom XML, OPML. Assert correct `Item` mapping.
- **Store:** CRUD against a temp SQLite DB; verify upsert preserves `read`,
  filter queries return correct subsets, `MarkAllRead` honors the filter.
- **Sync engine:** mock `Provider` (incl. an error-returning one) → assert
  fan-in, upsert, and that one failure doesn't sink the batch.
- **Render:** golden tests for representative HTML → output.
- **UI:** `Update()` unit tests for key messages (read toggle, filter change,
  view routing) + `teatest` golden snapshots of feed/reader views.
- Target ≥80% coverage on `model`, `store`, `source`, `syncengine`, `render`.

---

## 12. Build Phases (incremental, each independently runnable)

1. **Scaffold:** `go.mod`, layout, `config`, `model`, `store` (schema + repo),
   `Provider` interface. Unit-test the store.
2. **RSS first (no auth):** OPML import + gofeed provider → populate DB. Tiny
   debug command to dump the feed; validates the data path end-to-end.
3. **TUI core:** feed list + reader + render pipeline over cached items.
4. **Filtering + read state:** filter bar, quick toggles, read/unread persist.
5. **Gmail provider:** OAuth loopback flow, list + lazy body, multi-account.
6. **Sync engine:** background concurrent + periodic sync, status line, spinner.
7. **Polish:** help overlay, theming, resize handling, docs (README + setup),
   broaden tests.

---

## 13. Verification (how to prove it works)

- `go build ./...` and `go test ./...` green; `gofmt`/`go vet` clean.
- **RSS path:** point config at a sample OPML, launch, confirm entries appear,
  sorted newest-first; open one, confirm rich rendering; `o` opens the browser.
- **Gmail path:** run with a real account, complete OAuth in browser, confirm
  messages appear interleaved with RSS; open one, confirm body renders.
- **Read state:** mark items read (`m`), restart app, confirm state persisted
  (dimmed) and that a re-sync does **not** reset it.
- **Filters:** `e`/`r`/`u`/`a` and `/` search produce correct subsets.
- **Resilience:** add a bad feed URL / revoke one token; confirm the status line
  reports it and the rest of the feed still loads.

---

## Open items to confirm during build (assumptions made)

- Module path `github.com/steverhoton/renomail` (placeholder; adjust to remote).
- Default Gmail query `in:inbox newer_than:30d` and 5-minute sync interval.
- XDG paths (`~/.config/renomail`, `~/.local/share/renomail`).
- Pure-Go SQLite (`modernc.org/sqlite`) chosen to avoid a cgo toolchain.
