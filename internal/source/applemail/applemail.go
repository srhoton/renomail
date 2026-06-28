// Package applemail implements a macOS-local, strictly read-only source.Provider
// over Apple Mail's (Mail.app) on-disk data. It needs no credentials: it reads a
// private copy of Apple Mail's "Envelope Index" SQLite database to discover
// accounts and list each account's INBOX, and lazily parses the matching local
// .emlx message file for an item's body. renomail never writes to ~/Library/Mail.
//
// One Provider maps to one Apple Mail account's INBOX. Items reuse model.KindEmail
// (they are email), so they inherit the email treatment already wired through the
// engine, store, UI, and notifications. New arrivals surface through the ordinary
// pipeline — Fetch → store.UpsertItems → the per-sweep Slack digest and tmux ping.
//
// Reading Apple Mail requires the host terminal to have Full Disk Access (macOS
// TCC). Without it the index is unreadable; discovery reports ErrNoAccess so the
// caller can surface actionable guidance instead of crashing.
package applemail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered process-wide

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/source"
)

// Exported sentinels let the builder distinguish "no Full Disk Access" (worth an
// actionable warning) and "not macOS" (a stub build) from a genuine failure.
var (
	// ErrUnsupported is returned by Discover/New on non-macOS builds.
	ErrUnsupported = errors.New("apple mail is only available on macOS")
	// ErrNoAccess means ~/Library/Mail could not be read — almost always missing
	// Full Disk Access for the terminal (System Settings → Privacy & Security →
	// Full Disk Access).
	ErrNoAccess = errors.New("cannot read Apple Mail data; grant Full Disk Access to your terminal in System Settings → Privacy & Security → Full Disk Access")
)

// errNoMailData is internal: ~/Library/Mail (or its Envelope Index) simply is not
// present — Apple Mail is not set up. Callers treat it as "no accounts", silently.
var errNoMailData = errors.New("apple mail: no local data found")

// defaultLookback bounds a cold-start INBOX scan when the configured lookback is
// non-positive, so a first sweep never tries to read from the epoch.
const defaultLookback = 30 * 24 * time.Hour

// Account is a discovered Apple Mail account: its Mail-internal UUID (stable, taken
// from the mailbox URL) and a best-effort display name (the account's own email
// when resolvable, else a short label).
type Account struct {
	ID      string
	Display string
}

// Provider implements source.Provider for one Apple Mail account's INBOX. It holds
// no open handles: each Fetch/Body opens a fresh, private snapshot of the index, so
// the type is safe for concurrent use and never contends with a running Mail.app.
type Provider struct {
	root     string // ~/Library/Mail (injected for tests)
	acctID   string // Mail account UUID
	name     string // display name
	id       string // "applemail:" + acctID
	lookback time.Duration
}

// Compile-time assertion that Provider satisfies the source contract.
var _ source.Provider = (*Provider)(nil)

// mailRoot resolves the Apple Mail data root. It is a package var (not a direct
// call) so tests can point Discover at a fixture tree without touching the real
// ~/Library/Mail.
var mailRoot = defaultMailRoot

// Discover finds every Apple Mail account that owns an INBOX and returns one
// Provider per account. It returns (nil, nil) when Apple Mail is not set up,
// ErrNoAccess when the data is unreadable (Full Disk Access), and ErrUnsupported
// on non-macOS builds.
func Discover(ctx context.Context, lookback time.Duration) ([]*Provider, error) {
	root, err := mailRoot()
	if err != nil {
		return nil, err
	}
	return discoverFromRoot(ctx, root, lookback)
}

// discoverFromRoot is the testable core of Discover, operating on an explicit Mail
// root so tests can point it at a fixture tree.
func discoverFromRoot(ctx context.Context, root string, lookback time.Duration) ([]*Provider, error) {
	var accts []Account
	err := withIndex(root, func(db *sql.DB) error {
		var qerr error
		accts, qerr = queryAccounts(ctx, db)
		return qerr
	})
	if errors.Is(err, errNoMailData) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	providers := make([]*Provider, 0, len(accts))
	for _, a := range accts {
		providers = append(providers, newWithRoot(root, a, lookback))
	}
	return providers, nil
}

// newWithRoot constructs a Provider for one account rooted at a Mail directory,
// clamping a non-positive lookback to a sane default.
func newWithRoot(root string, a Account, lookback time.Duration) *Provider {
	if lookback <= 0 {
		lookback = defaultLookback
	}
	return &Provider{
		root:     root,
		acctID:   a.ID,
		name:     a.Display,
		id:       "applemail:" + a.ID,
		lookback: lookback,
	}
}

// ID returns the stable source identifier ("applemail:<account-uuid>"), distinct
// from Gmail and RSS ids and stable across runs.
func (p *Provider) ID() string { return p.id }

// Name returns the account's display name shown in the feed.
func (p *Provider) Name() string { return p.name }

// Kind reports that this provider yields email items (they are email; accounts are
// distinguished by Name, not Kind).
func (p *Provider) Kind() model.Kind { return model.KindEmail }

// Fetch lists the account's INBOX from a fresh index snapshot and returns one
// body-less item per message. On a cold start (zero since) it scans the lookback
// window; afterwards it asks only for messages at or after the last sync (inclusive,
// like the Gmail provider — the re-listed boundary message is idempotent on upsert).
// Bodies are loaded lazily by Body.
func (p *Provider) Fetch(ctx context.Context, since time.Time) ([]model.Item, error) {
	now := time.Now()
	lower := since
	if lower.IsZero() {
		lower = now.Add(-p.lookback)
	}

	var items []model.Item
	err := withIndex(p.root, func(db *sql.DB) error {
		mbox, err := inboxMailbox(ctx, db, p.acctID)
		if err != nil {
			return err
		}
		if mbox == 0 {
			return nil // account has no INBOX in this snapshot
		}
		items, err = queryInbox(ctx, db, p, mbox, lower.Unix(), now)
		return err
	})
	if errors.Is(err, errNoMailData) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("apple mail fetch %s: %w", p.name, err)
	}
	return items, nil
}

// Body lazily fills an item's content by locating and parsing its local .emlx file.
// The message row id is recovered from the snapshot by the item's native id (its
// RFC-822 Message-ID, or a "rowid:" fallback), then the .emlx is found under the
// account's INBOX mailbox. A message whose body is not present locally (a
// .partial.emlx that was never downloaded, or no file at all) leaves the body empty
// and returns nil — the reader falls back to the snippet; only a real read/parse
// failure is surfaced as an error.
func (p *Provider) Body(ctx context.Context, item *model.Item) error {
	rowid, found, err := p.resolveRowID(ctx, item.NativeID)
	if err != nil {
		return fmt.Errorf("apple mail body %s: %w", item.ID, err)
	}
	if !found {
		return nil
	}

	path, ok, err := findEmlx(p.root, p.acctID, rowid)
	if err != nil {
		return fmt.Errorf("apple mail body %s: %w", item.ID, err)
	}
	if !ok {
		return nil // body not downloaded locally
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("apple mail read %s: %w", path, err)
	}
	htmlBody, textBody, err := parseEmlx(data)
	if err != nil {
		return fmt.Errorf("apple mail parse %s: %w", path, err)
	}
	item.BodyHTML, item.BodyText = htmlBody, textBody
	return nil
}

// resolveRowID maps an item's native id to its Envelope Index row id (the .emlx
// filename). A "rowid:" native id is parsed directly and needs no index access at
// all; an RFC-822 Message-ID is looked up in the shared snapshot. found is false when
// the message is not in the index. Keeping the rowid path index-free is what lets a
// fast preview-pane scroll avoid touching the database.
func (p *Provider) resolveRowID(ctx context.Context, nativeID string) (rowid int64, found bool, err error) {
	if rest, ok := strings.CutPrefix(nativeID, "rowid:"); ok {
		n, perr := strconv.ParseInt(rest, 10, 64)
		return n, perr == nil && n > 0, nil
	}
	err = withIndex(p.root, func(db *sql.DB) error {
		r, ok, qerr := rowidByMessageID(ctx, db, nativeID)
		rowid, found = r, ok
		return qerr
	})
	if errors.Is(err, errNoMailData) {
		return 0, false, nil
	}
	return rowid, found, err
}

// withIndex runs fn against a private, read-only snapshot of Apple Mail's Envelope
// Index, reusing a process-wide cached copy that is refreshed only when the source
// changes. The snapshot is reference-held for the duration of fn so a concurrent
// refresh can never close the DB mid-query. It returns errNoMailData when there is no
// index and ErrNoAccess when the data is unreadable (Full Disk Access).
func withIndex(root string, fn func(*sql.DB) error) error {
	s, err := indexCache.acquire(root)
	if err != nil {
		return err
	}
	defer indexCache.release(s)
	return fn(s.db)
}

// snapshot is one immutable, private read-only copy of the Envelope Index and the DB
// opened over it. Once published it is never mutated; it is closed and its temp copy
// removed only after it has been superseded (retired) AND its last in-flight user has
// released it (refs == 0). That ordering is what makes a mid-sweep refresh safe for
// the concurrent account fetches that share the snapshot.
type snapshot struct {
	db      *sql.DB
	tmpDir  string
	src     string  // index path this snapshot is for
	key     statKey // change key when it was taken
	refs    int     // in-flight users (guarded by indexSnapshot.mu)
	retired bool    // superseded; close once refs hits 0
}

// indexSnapshot caches the current snapshot of the Envelope Index. The index is a
// single database shared by every account, and Mail keeps it open (WAL), so renomail
// reads a copy rather than the live file — never writing to ~/Library/Mail. Re-copying
// the (tens-of-MB) file on every Fetch/Body would be wasteful, and the engine fans
// accounts out concurrently, so one snapshot is shared process-wide and refreshed only
// when the source's change key shifts (new mail lands in the -wal sibling before any
// checkpoint, so the key covers both the main file and -wal). A superseded snapshot is
// reference-counted so an in-flight reader is never closed out from under it.
//
// The live snapshot is intentionally process-lived: a Provider holds no handles, so
// there is nothing to close per fetch. CloseCache releases it at shutdown; if that is
// not called, the OS reclaims the single open handle and temp dir at process exit.
type indexSnapshot struct {
	mu  sync.Mutex
	cur *snapshot
}

// statKey identifies a version of the index by the size+mtime of the main file and
// its -wal sibling. Any Mail write moves one of them, invalidating the snapshot.
type statKey struct {
	mainMod, mainSize int64
	walMod, walSize   int64
}

var indexCache indexSnapshot

// acquire returns the current snapshot — copying a fresh one only when Apple Mail's
// index has changed since the last copy (or on first use) — and increments its
// reference count. The caller MUST release the returned snapshot when done. The lock
// is held across a refresh copy so concurrent account fetches in one sweep share a
// single copy; the subsequent query work runs lock-free on the snapshot's DB.
func (c *indexSnapshot) acquire(root string) (*snapshot, error) {
	dataDir, err := latestMailDataDir(root)
	if err != nil {
		return nil, err
	}
	if dataDir == "" {
		return nil, errNoMailData
	}
	idx := filepath.Join(dataDir, "Envelope Index")
	key, err := indexStatKey(idx)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errNoMailData
		}
		if errors.Is(err, fs.ErrPermission) {
			return nil, ErrNoAccess
		}
		return nil, fmt.Errorf("apple mail stat index: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil && c.cur.src == idx && c.cur.key == key {
		c.cur.refs++
		return c.cur, nil
	}
	db, tmpDir, err := openSnapshot(idx)
	if err != nil {
		return nil, err
	}
	c.retireLocked() // supersede the previous snapshot (closed once its readers drain)
	c.cur = &snapshot{db: db, tmpDir: tmpDir, src: idx, key: key, refs: 1}
	return c.cur, nil
}

// release drops one reference to s, closing its DB and removing its temp copy once it
// has been retired and no readers remain.
func (c *indexSnapshot) release(s *snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s.refs--
	if s.refs == 0 && s.retired {
		closeSnapshot(s)
	}
}

// retireLocked marks the current snapshot superseded, closing it immediately when no
// readers are in flight (otherwise the last release closes it). The caller holds mu.
func (c *indexSnapshot) retireLocked() {
	if c.cur == nil {
		return
	}
	c.cur.retired = true
	if c.cur.refs == 0 {
		closeSnapshot(c.cur)
	}
	c.cur = nil
}

// closeSnapshot closes s's DB and removes its temp copy. The caller holds mu.
func closeSnapshot(s *snapshot) {
	if s.db != nil {
		_ = s.db.Close()
	}
	if s.tmpDir != "" {
		_ = os.RemoveAll(s.tmpDir)
	}
}

// CloseCache releases the cached Apple Mail index snapshot — closing its DB and
// removing its temp copy — so the last snapshot does not linger after shutdown. It is
// safe to call when nothing is cached and is idempotent; an in-flight reader defers the
// actual close to its release. It is best-effort at shutdown: a sweep that acquires
// after CloseCache returns recreates a snapshot, which the OS reclaims at process exit.
func CloseCache() {
	indexCache.mu.Lock()
	defer indexCache.mu.Unlock()
	indexCache.retireLocked()
}

// indexStatKey builds the change key from the main index file and its -wal sibling. A
// missing -wal contributes zeros (Mail not mid-write); a missing main file surfaces
// fs.ErrNotExist/fs.ErrPermission for the caller to classify.
func indexStatKey(idx string) (statKey, error) {
	fi, err := os.Stat(idx)
	if err != nil {
		return statKey{}, err
	}
	k := statKey{mainMod: fi.ModTime().UnixNano(), mainSize: fi.Size()}
	if wfi, werr := os.Stat(idx + "-wal"); werr == nil {
		k.walMod, k.walSize = wfi.ModTime().UnixNano(), wfi.Size()
	}
	return k, nil
}

// openSnapshot copies the index (plus any -wal/-shm) into a fresh temp dir and opens
// the copy read-only. On failure it removes the temp dir; a permission error maps to
// ErrNoAccess. The returned tmpDir is owned by the cache and removed on the next
// refresh.
func openSnapshot(idx string) (*sql.DB, string, error) {
	tmpDir, err := os.MkdirTemp("", "renomail-applemail-")
	if err != nil {
		return nil, "", fmt.Errorf("apple mail snapshot: %w", err)
	}
	dst := filepath.Join(tmpDir, "index.sqlite")
	if err := snapshotIndex(idx, dst); err != nil {
		_ = os.RemoveAll(tmpDir)
		if errors.Is(err, fs.ErrPermission) {
			return nil, "", ErrNoAccess
		}
		return nil, "", err
	}
	// query_only enforces the read-only contract; the same _pragma DSN style the
	// store uses keeps the driver behavior consistent.
	db, err := sql.Open("sqlite", dst+"?_pragma=busy_timeout(5000)&_pragma=query_only(true)")
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, "", fmt.Errorf("apple mail open index: %w", err)
	}
	return db, tmpDir, nil
}

// snapshotIndex copies the index database and, when present, its -wal and -shm
// siblings so the opened copy sees the most recent committed (WAL) state.
func snapshotIndex(srcIndex, dstIndex string) error {
	if err := copyFile(srcIndex, dstIndex); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := copyFile(srcIndex+suffix, dstIndex+suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// copyFile copies src to dst. A missing src surfaces as fs.ErrNotExist so callers
// can treat optional siblings (-wal/-shm) as absent.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// latestMailDataDir returns "<root>/V<n>/MailData" for the highest V<n>, "" when
// Apple Mail is absent (no root, no V dir), and ErrNoAccess on a permission error.
func latestMailDataDir(root string) (string, error) {
	vdir, err := latestVDir(root)
	if err != nil || vdir == "" {
		return "", err
	}
	return filepath.Join(vdir, "MailData"), nil
}

// latestVDir returns "<root>/V<n>" for the highest version directory under root.
// Apple Mail bumps this number per macOS release (currently V10); never hardcode it.
func latestVDir(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		if errors.Is(err, fs.ErrPermission) {
			return "", ErrNoAccess
		}
		return "", fmt.Errorf("apple mail read %s: %w", root, err)
	}
	best, bestName := -1, ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if n, ok := parseVersionDir(e.Name()); ok && n > best {
			best, bestName = n, e.Name()
		}
	}
	if bestName == "" {
		return "", nil
	}
	return filepath.Join(root, bestName), nil
}

// parseVersionDir reports whether name is a Mail version directory ("V10" -> 10).
func parseVersionDir(name string) (int, bool) {
	if len(name) < 2 || (name[0] != 'V' && name[0] != 'v') {
		return 0, false
	}
	n, err := strconv.Atoi(name[1:])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// findEmlx locates the .emlx (preferred) or .partial.emlx file for a message row id
// under the account's INBOX mailbox. The on-disk path shards by reversed row-id
// digits in a layout that varies, so it walks the bounded INBOX.mbox subtree and
// matches by filename rather than re-deriving the path. ok is false when no file is
// present (body not downloaded).
func findEmlx(root, acctID string, rowid int64) (path string, ok bool, err error) {
	vdir, err := latestVDir(root)
	if err != nil || vdir == "" {
		return "", false, err
	}
	base := filepath.Join(vdir, acctID, "INBOX.mbox")
	id := strconv.FormatInt(rowid, 10)
	full := id + ".emlx"
	partial := id + ".partial.emlx"

	var hit string
	walkErr := filepath.WalkDir(base, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if errors.Is(werr, fs.ErrNotExist) {
				return fs.SkipAll // INBOX.mbox absent — nothing to find
			}
			return nil // tolerate a transient/permission hiccup on one entry
		}
		if d.IsDir() {
			return nil
		}
		switch d.Name() {
		case full:
			hit = p
			return fs.SkipAll
		case partial:
			if hit == "" {
				hit = p // keep looking for a full .emlx, but remember the partial
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
		return "", false, walkErr
	}
	if hit == "" {
		return "", false, nil
	}
	return hit, true, nil
}
