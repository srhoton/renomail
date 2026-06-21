# 02 — Domain Model & SQLite Store

## Goal

Define the unified domain types and the persistence layer that every provider and
the UI depend on. After this step the app can store, upsert, query, and mutate
feed items in a local SQLite database — with read/unread state preserved across
re-fetches — and the `Provider` interface exists for providers to implement.

## Prerequisites

- Step 01 (module, config, paths).

## Deliverables

```
internal/model/item.go        # Item, Source, Kind, ReadState, Filter, ID hashing
internal/model/item_test.go
internal/store/store.go        # Open, schema/migrations, repo methods
internal/store/store_test.go
internal/source/source.go      # Provider interface
```

```bash
go get modernc.org/sqlite@latest
```

## Design detail

### Domain types (`internal/model/item.go`)

Mirror `DESIGN.md` §3 exactly. Add the stable-ID helper that providers use so the
same message/entry always maps to the same row (enables idempotent upserts).

> **`native_id` adopted in this step.** `Item` carries a `NativeID string`
> (Gmail message id / RSS guid|link) and the `items` table a matching
> `native_id TEXT` column, so Gmail's lazy `Body()` (step 06) recovers the
> message id directly rather than parsing the web URL. This resolves the open
> item flagged in `06-gmail-oauth.md`; the schema ships with it from v1 (no
> migration needed). `DESIGN.md` §3/§5 are updated to match.

```go
type Kind string
const (
    KindEmail Kind = "email"
    KindRSS   Kind = "rss"
)

type ReadState int
const (
    ReadAny ReadState = iota
    ReadUnreadOnly
    ReadReadOnly
)

type Item struct {
    ID         string
    Kind       Kind
    SourceID   string
    SourceName string
    Author     string
    Title      string
    Snippet    string
    URL        string
    NativeID   string // Gmail message id / RSS guid|link
    Published  time.Time
    Fetched    time.Time
    Read       bool
    BodyHTML   string
    BodyText   string
}

type Source struct {
    ID, Name     string
    Kind         Kind
    LastSync     time.Time
    ETag         string
    LastModified string
}

type Filter struct {
    Kinds     map[Kind]bool   // empty/nil = all kinds
    SourceIDs map[string]bool // empty/nil = all sources
    Read      ReadState
    Search    string
}

// StableID derives a deterministic Item.ID from the source and the provider's
// native id (Gmail message id / RSS entry guid|link). Same inputs => same id.
func StableID(sourceID, nativeID string) string {
    sum := sha256.Sum256([]byte(sourceID + "\x00" + nativeID))
    return hex.EncodeToString(sum[:])
}
```

### Provider interface (`internal/source/source.go`)

Lifted from `DESIGN.md` §4. Both RSS (03) and Gmail (06) implement it; the sync
engine (07) consumes it.

```go
package source

type Provider interface {
    ID() string
    Name() string
    Kind() model.Kind
    Fetch(ctx context.Context, since time.Time) ([]model.Item, error)
    Body(ctx context.Context, item *model.Item) error
}
```

### Store (`internal/store/store.go`)

Pure-Go driver registers as `"sqlite"`. Times are stored as Unix seconds
(INTEGER) for cheap sorting/filtering. Schema and indexes per `DESIGN.md` §5.

```go
import _ "modernc.org/sqlite"

type Store struct{ db *sql.DB }

const schemaVersion = 1

func Open(path string) (*Store, error) {
    db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
    if err != nil { return nil, fmt.Errorf("open db: %w", err) }
    s := &Store{db: db}
    if err := s.migrate(); err != nil { return nil, err }
    return s, nil
}

// migrate creates tables if absent and applies version upgrades using meta.schema_version.
func (s *Store) migrate() error { /* CREATE TABLE IF NOT EXISTS ...; set/check version */ }
```

#### Upsert that preserves local read state

The critical rule (`DESIGN.md` §5): re-fetching must never reset `read`. Insert
new rows; on conflict, update content columns but **leave `read` untouched**.

```go
func (s *Store) UpsertItems(ctx context.Context, items []model.Item) error {
    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil { return fmt.Errorf("begin: %w", err) }
    defer tx.Rollback()

    const q = `
INSERT INTO items
  (id, kind, source_id, source_name, author, title, snippet, url,
   published, fetched, read, body_html, body_text)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  source_name = excluded.source_name,
  author      = excluded.author,
  title       = excluded.title,
  snippet     = excluded.snippet,
  url         = excluded.url,
  published   = excluded.published,
  fetched     = excluded.fetched,
  body_html   = CASE WHEN excluded.body_html <> '' THEN excluded.body_html ELSE items.body_html END,
  body_text   = CASE WHEN excluded.body_text <> '' THEN excluded.body_text ELSE items.body_text END
  -- NOTE: 'read' deliberately omitted so local state is preserved.
`
    stmt, err := tx.PrepareContext(ctx, q)
    if err != nil { return fmt.Errorf("prepare upsert: %w", err) }
    defer stmt.Close()
    for _, it := range items {
        if _, err := stmt.ExecContext(ctx,
            it.ID, it.Kind, it.SourceID, it.SourceName, it.Author, it.Title,
            it.Snippet, it.URL, it.Published.Unix(), it.Fetched.Unix(),
            boolToInt(it.Read), it.BodyHTML, it.BodyText); err != nil {
            return fmt.Errorf("upsert %s: %w", it.ID, err)
        }
    }
    return tx.Commit()
}
```

#### Query with a parameterized WHERE builder

```go
func (s *Store) Query(ctx context.Context, f model.Filter) ([]model.Item, error) {
    var where []string
    var args []any

    if len(f.Kinds) > 0 {
        ph, vals := inClause(f.Kinds)            // builds "kind IN (?,?)" + values
        where = append(where, "kind "+ph); args = append(args, vals...)
    }
    if len(f.SourceIDs) > 0 {
        ph, vals := inClause(f.SourceIDs)
        where = append(where, "source_id "+ph); args = append(args, vals...)
    }
    switch f.Read {
    case model.ReadUnreadOnly: where = append(where, "read = 0")
    case model.ReadReadOnly:   where = append(where, "read = 1")
    }
    if f.Search != "" {
        where = append(where, "(title LIKE ? OR author LIKE ? OR snippet LIKE ? OR body_text LIKE ?)")
        like := "%" + f.Search + "%"
        args = append(args, like, like, like, like)
    }

    q := "SELECT id,kind,source_id,source_name,author,title,snippet,url,published,fetched,read,body_html,body_text FROM items"
    if len(where) > 0 { q += " WHERE " + strings.Join(where, " AND ") }
    q += " ORDER BY published DESC"
    // scan rows into []model.Item (convert Unix ints back to time.Time) ...
}
```

#### Remaining methods

```go
func (s *Store) SetRead(ctx context.Context, id string, read bool) error
func (s *Store) MarkAllRead(ctx context.Context, f model.Filter) error // reuse the WHERE builder; UPDATE items SET read=1
func (s *Store) GetBody(ctx context.Context, id string) (html, text string, err error)
func (s *Store) SetBody(ctx context.Context, id, html, text string) error
func (s *Store) GetSource(ctx context.Context, id string) (model.Source, bool, error)
func (s *Store) UpsertSource(ctx context.Context, src model.Source) error
func (s *Store) Close() error
```

> Refactor note: `MarkAllRead` and `Query` share the WHERE builder — extract a
> `buildWhere(Filter) (clause string, args []any)` helper and reuse it.

## Implementation flow

1. Add `modernc.org/sqlite`.
2. Implement `model/item.go` (types + `StableID`).
3. Implement `source/source.go` (`Provider`).
4. Implement `store/store.go`: `Open`, `migrate`, `buildWhere`, `UpsertItems`,
   `Query`, `SetRead`, `MarkAllRead`, body + source methods, `Close`.
5. Write `store_test.go` against a temp DB.
6. `gofmt`/`vet`/`build`/`test`.

## Validation criteria

- `go build ./...`, `go vet`, `gofmt` clean.
- **Store tests (temp DB via `t.TempDir()`):**
  - Insert items → `Query(ReadAny)` returns them newest-first by `Published`.
  - Mark one read via `SetRead`, re-`UpsertItems` the same ids with fresh content
    → content updates **but `read` stays true** (the core regression guard).
  - `Query` honors `Kinds`, `SourceIDs`, `Read`, and `Search` independently and
    in combination.
  - `MarkAllRead` with a filter only flips the matching subset.
  - `SetBody`/`GetBody` round-trip; upsert with empty body does not clobber an
    existing stored body.
  - `UpsertSource`/`GetSource` round-trip incl. `LastSync`, `ETag`,
    `LastModified`.
- **Model test:** `StableID` is deterministic and differs across distinct inputs.

## Done checklist

- [ ] Domain types + `StableID` implemented and tested.
- [ ] `Provider` interface defined.
- [ ] Store implemented with read-preserving upsert and parameterized queries.
- [ ] Store tests cover read-preservation, all filter dimensions, mark-all-read,
      body/source round-trips.
- [ ] `gofmt`/`vet`/`test` green.
