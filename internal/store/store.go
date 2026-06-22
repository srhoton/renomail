// Package store is the SQLite repository for cached items, sources, and local
// read/unread state. It uses the pure-Go modernc.org/sqlite driver (no cgo).
// Times are persisted as Unix seconds for cheap sorting and filtering. The
// central invariant: re-fetching content via UpsertItems never resets an item's
// local read flag.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/srhoton/renomail/internal/model"
)

// schemaVersion is the current on-disk schema revision, tracked in meta so that
// future versions can migrate forward and refuse to open a newer database.
const schemaVersion = 1

// Store is a handle to the SQLite-backed item/source repository.
type Store struct {
	db *sql.DB
}

// Open opens the database at path using a background context. It is a
// convenience wrapper around OpenContext for boot-time callers.
func Open(path string) (*Store, error) {
	return OpenContext(context.Background(), path)
}

// OpenContext opens (creating if absent) the SQLite database at path and
// applies the schema. A busy timeout and WAL journaling are enabled for
// resilience under the concurrent reads/writes the sync engine and UI perform.
// The context governs the schema migration work.
func OpenContext(ctx context.Context, path string) (*Store, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db %s: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

const createSchema = `
CREATE TABLE IF NOT EXISTS items (
  id TEXT PRIMARY KEY, kind TEXT, source_id TEXT, source_name TEXT,
  author TEXT, title TEXT, snippet TEXT, url TEXT, native_id TEXT,
  published INTEGER, fetched INTEGER,
  read INTEGER DEFAULT 0, body_html TEXT, body_text TEXT
);
CREATE INDEX IF NOT EXISTS idx_items_published ON items(published DESC);
CREATE INDEX IF NOT EXISTS idx_items_kind      ON items(kind);
CREATE INDEX IF NOT EXISTS idx_items_source    ON items(source_id);
-- The read filter is always combined with "ORDER BY published DESC, id DESC", so a
-- composite (read, published DESC) lets a read-filtered query use one index for both
-- the filter and the dominant published ordering (only the id tie-break still sorts).
-- Its leading "read" column also serves bare "read = ?" lookups, making the old
-- single-column idx_items_read redundant; drop it.
DROP INDEX IF EXISTS idx_items_read;
CREATE INDEX IF NOT EXISTS idx_items_read_published ON items(read, published DESC);

CREATE TABLE IF NOT EXISTS sources (
  id TEXT PRIMARY KEY, kind TEXT, name TEXT,
  last_sync INTEGER, etag TEXT, last_modified TEXT
);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);
`

// migrate creates the schema if absent and reconciles the stored schema
// version. A fresh database is stamped with the current version; a database
// written by a newer version is refused rather than silently downgraded.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, createSchema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO meta(key, value) VALUES ('schema_version', ?)`,
			strconv.Itoa(schemaVersion),
		); err != nil {
			return fmt.Errorf("stamp schema version: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("read schema version: %w", err)
	}

	have, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("parse schema version %q: %w", raw, err)
	}
	if have > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported %d", have, schemaVersion)
	}
	// have == schemaVersion: nothing to do. Future have < schemaVersion
	// upgrades are applied here.
	return nil
}

// listColumns is the column set for list/feed queries. It deliberately omits
// body_html/body_text: the list view never needs bodies (they are loaded lazily
// via GetBody), and excluding them avoids pulling every cached body into memory
// on a full-feed query.
const listColumns = `id, kind, source_id, source_name, author, title, snippet,
	url, native_id, published, fetched, read`

// UpsertItems inserts or updates items in a single transaction. On conflict the
// content columns are refreshed, but the read flag is deliberately left
// untouched so local read/unread state survives re-fetching. A re-fetch that
// carries an empty body does not clobber a previously stored body.
func (s *Store) UpsertItems(ctx context.Context, items []model.Item) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const q = `
INSERT INTO items
  (id, kind, source_id, source_name, author, title, snippet, url, native_id,
   published, fetched, read, body_html, body_text)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  source_name = excluded.source_name,
  author      = excluded.author,
  title       = excluded.title,
  snippet     = excluded.snippet,
  url         = excluded.url,
  native_id   = excluded.native_id,
  published   = excluded.published,
  fetched     = excluded.fetched,
  body_html   = CASE WHEN excluded.body_html <> '' THEN excluded.body_html ELSE items.body_html END,
  body_text   = CASE WHEN excluded.body_text <> '' THEN excluded.body_text ELSE items.body_text END
` // NOTE: 'read' deliberately omitted so local state is preserved.

	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return fmt.Errorf("prepare upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, it := range items {
		if _, err := stmt.ExecContext(ctx,
			it.ID, string(it.Kind), it.SourceID, it.SourceName, it.Author,
			it.Title, it.Snippet, it.URL, it.NativeID,
			it.Published.Unix(), it.Fetched.Unix(),
			boolToInt(it.Read), it.BodyHTML, it.BodyText); err != nil {
			return fmt.Errorf("upsert %s: %w", it.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upsert: %w", err)
	}
	return nil
}

// Query returns the items matching f, newest first by published time, with id
// as a stable tie-breaker for items sharing a published timestamp. A zero Filter
// returns every item. Bodies are not populated (use GetBody for full content).
func (s *Store) Query(ctx context.Context, f model.Filter) ([]model.Item, error) {
	clause, args := buildWhere(f)
	q := "SELECT " + listColumns + " FROM items"
	if clause != "" {
		q += " WHERE " + clause
	}
	q += " ORDER BY published DESC, id DESC"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query items: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]model.Item, 0, 256)
	for rows.Next() {
		it, err := scanListItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate items: %w", err)
	}
	return items, nil
}

// SetRead sets the local read flag for a single item.
func (s *Store) SetRead(ctx context.Context, id string, read bool) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE items SET read = ? WHERE id = ?`, boolToInt(read), id); err != nil {
		return fmt.Errorf("set read %s: %w", id, err)
	}
	return nil
}

// MarkAllRead marks every item matching f as read. With a zero Filter this
// marks the entire store read.
func (s *Store) MarkAllRead(ctx context.Context, f model.Filter) error {
	clause, args := buildWhere(f)
	q := "UPDATE items SET read = 1"
	if clause != "" {
		q += " WHERE " + clause
	}
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("mark all read: %w", err)
	}
	return nil
}

// GetBody returns the stored html and text bodies for an item. A missing item
// returns sql.ErrNoRows wrapped with context.
func (s *Store) GetBody(ctx context.Context, id string) (html, text string, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT body_html, body_text FROM items WHERE id = ?`, id).Scan(&html, &text)
	if err != nil {
		return "", "", fmt.Errorf("get body %s: %w", id, err)
	}
	return html, text, nil
}

// SetBody stores the html and text bodies for an item.
func (s *Store) SetBody(ctx context.Context, id, html, text string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE items SET body_html = ?, body_text = ? WHERE id = ?`,
		html, text, id); err != nil {
		return fmt.Errorf("set body %s: %w", id, err)
	}
	return nil
}

// GetSource returns the stored source by id. The boolean is false (with a nil
// error) when no such source exists.
func (s *Store) GetSource(ctx context.Context, id string) (model.Source, bool, error) {
	var (
		src      model.Source
		kind     string
		lastSync int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, kind, name, last_sync, etag, last_modified FROM sources WHERE id = ?`, id).
		Scan(&src.ID, &kind, &src.Name, &lastSync, &src.ETag, &src.LastModified)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return model.Source{}, false, nil
	case err != nil:
		return model.Source{}, false, fmt.Errorf("get source %s: %w", id, err)
	}
	src.Kind = model.Kind(kind)
	src.LastSync = time.Unix(lastSync, 0).UTC()
	return src, true, nil
}

// UpsertSource inserts or replaces a source's metadata and sync bookkeeping.
func (s *Store) UpsertSource(ctx context.Context, src model.Source) error {
	if _, err := s.db.ExecContext(ctx, upsertSourceSQL,
		src.ID, string(src.Kind), src.Name, src.LastSync.Unix(),
		src.ETag, src.LastModified); err != nil {
		return fmt.Errorf("upsert source %s: %w", src.ID, err)
	}
	return nil
}

// upsertSourceSQL is the shared INSERT…ON CONFLICT statement for persisting a
// source's metadata and sync bookkeeping, used by both UpsertSource and the
// batched UpsertSources.
const upsertSourceSQL = `
INSERT INTO sources (id, kind, name, last_sync, etag, last_modified)
VALUES (?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  kind          = excluded.kind,
  name          = excluded.name,
  last_sync     = excluded.last_sync,
  etag          = excluded.etag,
  last_modified = excluded.last_modified`

// UpsertSources persists many sources in a single transaction, so a sync sweep
// commits all its per-source state once rather than contending on SQLite's single
// writer with one transaction per source. An empty slice is a no-op.
func (s *Store) UpsertSources(ctx context.Context, srcs []model.Source) error {
	if len(srcs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, upsertSourceSQL)
	if err != nil {
		return fmt.Errorf("prepare upsert sources: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, src := range srcs {
		if _, err := stmt.ExecContext(ctx,
			src.ID, string(src.Kind), src.Name, src.LastSync.Unix(),
			src.ETag, src.LastModified); err != nil {
			return fmt.Errorf("upsert source %s: %w", src.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upsert sources: %w", err)
	}
	return nil
}

// buildWhere assembles the shared parameterized predicate used by both Query
// and MarkAllRead. It returns the bare clause (no leading WHERE) and the
// matching argument values; an empty clause means "match everything".
func buildWhere(f model.Filter) (clause string, args []any) {
	var preds []string

	if len(f.Kinds) > 0 {
		keys := make([]string, 0, len(f.Kinds))
		for k := range f.Kinds {
			keys = append(keys, string(k))
		}
		ph, vals := inClause(keys)
		preds = append(preds, "kind "+ph)
		args = append(args, vals...)
	}
	if len(f.SourceIDs) > 0 {
		keys := make([]string, 0, len(f.SourceIDs))
		for k := range f.SourceIDs {
			keys = append(keys, k)
		}
		ph, vals := inClause(keys)
		preds = append(preds, "source_id "+ph)
		args = append(args, vals...)
	}
	switch f.Read {
	case model.ReadUnreadOnly:
		preds = append(preds, "read = 0")
	case model.ReadReadOnly:
		preds = append(preds, "read = 1")
	case model.ReadAny:
		// no predicate
	}
	if f.Search != "" {
		// Escape LIKE metacharacters so a search term containing % or _ matches
		// literally rather than acting as a wildcard. ESCAPE '\' names the
		// escape character for each LIKE.
		preds = append(preds, `(title LIKE ? ESCAPE '\' OR author LIKE ? ESCAPE '\' OR snippet LIKE ? ESCAPE '\' OR body_text LIKE ? ESCAPE '\')`)
		like := "%" + escapeLike(f.Search) + "%"
		args = append(args, like, like, like, like)
	}

	return strings.Join(preds, " AND "), args
}

// escapeLike escapes the SQL LIKE metacharacters (% and _) and the escape
// character itself (\) so that a user search term is matched literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// inClause builds an "IN (?,?,…)" placeholder fragment and its argument slice.
// Keys are sorted so the generated SQL and argument order are deterministic
// (Go map iteration order is randomized).
func inClause(keys []string) (placeholders string, args []any) {
	sort.Strings(keys)
	marks := make([]string, len(keys))
	args = make([]any, len(keys))
	for i, k := range keys {
		marks[i] = "?"
		args[i] = k
	}
	return "IN (" + strings.Join(marks, ",") + ")", args
}

// scanListItem reads one item row in the listColumns layout, converting Unix
// seconds back to time.Time and the read integer back to a bool. BodyHTML and
// BodyText are left empty; the list query does not select them.
func scanListItem(rows *sql.Rows) (model.Item, error) {
	var (
		it        model.Item
		kind      string
		published int64
		fetched   int64
		read      int
	)
	if err := rows.Scan(
		&it.ID, &kind, &it.SourceID, &it.SourceName, &it.Author, &it.Title,
		&it.Snippet, &it.URL, &it.NativeID, &published, &fetched, &read); err != nil {
		return model.Item{}, fmt.Errorf("scan item: %w", err)
	}
	it.Kind = model.Kind(kind)
	it.Published = time.Unix(published, 0).UTC()
	it.Fetched = time.Unix(fetched, 0).UTC()
	it.Read = read != 0
	return it, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
