package applemail

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/srhoton/renomail/internal/model"
)

const (
	acctA = "AAAAAAAA-0000-0000-0000-000000000001" // has Sent → display = me@a.example
	acctB = "BBBBBBBB-0000-0000-0000-000000000002" // no Sent, has To recipient → me@b.example
	acctC = "CCCCCCCC-0000-0000-0000-000000000003" // nothing → fallback label
	acctG = "DDDDDDDD-0000-0000-0000-000000000004" // Gmail-style: INBOX is a label view → owner@g.com
)

const (
	tOld = int64(1700000000) // older INBOX message
	tNew = int64(1700000500) // newer INBOX message
)

// writeFixtureRoot builds a minimal Apple Mail tree: an Envelope Index shaped like
// the real one (only the columns the queries touch) plus the directory layout under
// which Body looks for .emlx files. It returns the Mail root (~/Library/Mail analog).
func writeFixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mailData := filepath.Join(root, "V10", "MailData")
	if err := os.MkdirAll(mailData, 0o755); err != nil {
		t.Fatalf("mkdir MailData: %v", err)
	}
	dbPath := filepath.Join(mailData, "Envelope Index")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE mailboxes (ROWID INTEGER PRIMARY KEY, url TEXT, source INTEGER)`,
		`CREATE TABLE addresses (ROWID INTEGER PRIMARY KEY, address TEXT, comment TEXT)`,
		`CREATE TABLE subjects (ROWID INTEGER PRIMARY KEY, subject TEXT)`,
		`CREATE TABLE summaries (ROWID INTEGER PRIMARY KEY, summary TEXT)`,
		`CREATE TABLE message_global_data (ROWID INTEGER PRIMARY KEY, message_id_header TEXT)`,
		`CREATE TABLE recipients (ROWID INTEGER PRIMARY KEY, message INTEGER, address INTEGER, type INTEGER)`,
		`CREATE TABLE labels (message_id INTEGER, mailbox_id INTEGER, PRIMARY KEY(message_id, mailbox_id))`,
		`CREATE TABLE messages (ROWID INTEGER PRIMARY KEY, global_message_id INTEGER, sender INTEGER,
		   subject_prefix TEXT, subject INTEGER, summary INTEGER, date_received INTEGER, mailbox INTEGER,
		   deleted INTEGER, read INTEGER DEFAULT 0)`,

		// Mailboxes: real INBOXes (source NULL) for A/B/C; account G is Gmail-style —
		// its INBOX (5) is a label view whose source points at [Gmail]/All Mail (6).
		`INSERT INTO mailboxes (ROWID, url, source) VALUES
		   (1, 'imap://` + acctA + `/INBOX', NULL),
		   (2, 'imap://` + acctA + `/Sent Messages', NULL),
		   (3, 'imap://` + acctB + `/INBOX', NULL),
		   (4, 'imap://` + acctC + `/INBOX', NULL),
		   (5, 'imap://` + acctG + `/INBOX', 6),
		   (6, 'imap://` + acctG + `/%5BGmail%5D/All%20Mail', NULL)`,

		`INSERT INTO addresses (ROWID, address, comment) VALUES
		   (1, 'alice@x.com', 'Alice'),
		   (2, 'bob@y.com', ''),
		   (3, 'me@a.example', ''),
		   (4, 'me@b.example', ''),
		   (5, 'owner@g.com', '')`,

		`INSERT INTO subjects (ROWID, subject) VALUES (1, 'Hello'), (2, 'Meeting')`,
		`INSERT INTO summaries (ROWID, summary) VALUES (1, 'hi there'), (2, 'agenda')`,
		`INSERT INTO message_global_data (ROWID, message_id_header) VALUES
		   (100, '<m10@x.com>'), (101, '<m11@y.com>'), (102, '<m12@b.example>'),
		   (103, '<m30@g.com>'), (104, '<m31@g.com>')`,

		// A/INBOX: msg 10 (new, unread), msg 11 (old, read, "Re:"), msg 13 (deleted → excluded).
		`INSERT INTO messages (ROWID, global_message_id, sender, subject_prefix, subject, summary, date_received, mailbox, deleted, read) VALUES
		   (10, 100, 1, '',    1, 1, ` + itoa(tNew) + `, 1, 0, 0),
		   (11, 101, 2, 'Re:', 2, 2, ` + itoa(tOld) + `, 1, 0, 1),
		   (13, 0,   1, '',    1, 1, ` + itoa(tNew) + `, 1, 1, 0)`,
		// A/Sent: msg 20 from me@a.example (owner address).
		`INSERT INTO messages (ROWID, global_message_id, sender, subject_prefix, subject, summary, date_received, mailbox, deleted, read) VALUES
		   (20, 0, 3, '', 1, 1, 1690000000, 2, 0, 1)`,
		// B/INBOX: msg 12, with a To recipient me@b.example.
		`INSERT INTO messages (ROWID, global_message_id, sender, subject_prefix, subject, summary, date_received, mailbox, deleted, read) VALUES
		   (12, 102, 1, '', 1, 1, ` + itoa(tOld) + `, 3, 0, 0)`,
		// G (Gmail): messages stored in All Mail (6); INBOX membership via labels → 5.
		// msg 30 (new, unread) and 31 (old, read) are in the inbox; msg 32 is archived
		// (in All Mail, no INBOX label) and must NOT surface.
		`INSERT INTO messages (ROWID, global_message_id, sender, subject_prefix, subject, summary, date_received, mailbox, deleted, read) VALUES
		   (30, 103, 1, '', 1, 1, ` + itoa(tNew) + `, 6, 0, 0),
		   (31, 104, 2, '', 2, 2, ` + itoa(tOld) + `, 6, 0, 1),
		   (32, 0,   1, '', 1, 1, ` + itoa(tNew) + `, 6, 0, 0)`,
		`INSERT INTO labels (message_id, mailbox_id) VALUES (30, 5), (31, 5)`,
		`INSERT INTO recipients (ROWID, message, address, type) VALUES
		   (1, 12, 4, 0),
		   (2, 30, 5, 0),
		   (3, 31, 5, 0)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed fixture (%.40s…): %v", s, err)
		}
	}
	return root
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// writeEmlx writes a fixture .emlx for a message row id under the account's INBOX
// mailbox (the common iCloud/Exchange layout).
func writeEmlx(t *testing.T, root, acctID string, rowid int64, rfc822 string) {
	t.Helper()
	writeEmlxIn(t, root, acctID, []string{"INBOX.mbox"}, rowid, rfc822)
}

// writeEmlxIn writes a fixture .emlx under an arbitrary mailbox path (e.g.
// {"[Gmail].mbox", "All Mail.mbox"} for a Gmail inbox message that physically lives in
// All Mail), mirroring the real on-disk layout.
func writeEmlxIn(t *testing.T, root, acctID string, mailboxPath []string, rowid int64, rfc822 string) {
	t.Helper()
	parts := append([]string{root, "V10", acctID}, mailboxPath...)
	parts = append(parts, "Store", "Data", "Messages")
	dir := filepath.Join(parts...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir emlx dir: %v", err)
	}
	body := itoa(int64(len(rfc822))) + "\n" + rfc822 + "<?xml version=\"1.0\"?><plist></plist>"
	path := filepath.Join(dir, itoa(rowid)+".emlx")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write emlx: %v", err)
	}
}

func TestDiscoverFromRoot_accountsAndDisplay(t *testing.T) {
	root := writeFixtureRoot(t)

	provs, err := discoverFromRoot(context.Background(), root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("discoverFromRoot: %v", err)
	}
	if len(provs) != 4 {
		t.Fatalf("got %d providers, want 4", len(provs))
	}
	// Sorted by display: "Apple Mail (CCCCCCCC)" < "me@a.example" < "me@b.example" <
	// "owner@g.com". The Gmail account G resolves to owner@g.com via the label-aware
	// To-recipient query (its inbox would be empty under the old mailbox= query).
	wantNames := []string{"Apple Mail (CCCCCCCC)", "me@a.example", "me@b.example", "owner@g.com"}
	wantIDs := []string{"applemail:" + acctC, "applemail:" + acctA, "applemail:" + acctB, "applemail:" + acctG}
	for i, p := range provs {
		if p.Name() != wantNames[i] {
			t.Errorf("provs[%d].Name() = %q, want %q", i, p.Name(), wantNames[i])
		}
		if p.ID() != wantIDs[i] {
			t.Errorf("provs[%d].ID() = %q, want %q", i, p.ID(), wantIDs[i])
		}
		if p.Kind() != model.KindEmail {
			t.Errorf("provs[%d].Kind() = %q, want email", i, p.Kind())
		}
	}
}

func providerFor(t *testing.T, provs []*Provider, id string) *Provider {
	t.Helper()
	for _, p := range provs {
		if p.ID() == "applemail:"+id {
			return p
		}
	}
	t.Fatalf("no provider for %s", id)
	return nil
}

func TestFetch_incrementalOrderingAndMapping(t *testing.T) {
	root := writeFixtureRoot(t)
	provs, err := discoverFromRoot(context.Background(), root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("discoverFromRoot: %v", err)
	}
	p := providerFor(t, provs, acctA)

	// since just before the older message → both INBOX messages, newest first,
	// deleted excluded.
	items, err := p.Fetch(context.Background(), time.Unix(tOld-1, 0))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (deleted excluded)", len(items))
	}

	got := items[0]
	if got.Title != "Hello" || got.Author != "Alice <alice@x.com>" || got.Snippet != "hi there" {
		t.Errorf("item0 = %+v", got)
	}
	if got.Read {
		t.Errorf("item0 (msg 10) Read = true, want false (unread imported from Apple Mail)")
	}
	if got.NativeID != "<m10@x.com>" || got.URL != "message://%3Cm10@x.com%3E" {
		t.Errorf("item0 nativeID/url = %q / %q", got.NativeID, got.URL)
	}
	if !got.Published.Equal(time.Unix(tNew, 0).UTC()) {
		t.Errorf("item0 Published = %v, want %v", got.Published, time.Unix(tNew, 0).UTC())
	}
	if got.ID != model.StableID("applemail:"+acctA, "<m10@x.com>") {
		t.Errorf("item0 ID = %q", got.ID)
	}
	if got.Kind != model.KindEmail || got.SourceID != "applemail:"+acctA || got.SourceName != "me@a.example" {
		t.Errorf("item0 kind/source = %q / %q / %q", got.Kind, got.SourceID, got.SourceName)
	}

	if items[1].Title != "Re: Meeting" || items[1].Author != "bob@y.com" {
		t.Errorf("item1 = %+v", items[1])
	}
	if !items[1].Read {
		t.Errorf("item1 (msg 11) Read = false, want true (read imported from Apple Mail)")
	}

	// since at the newer timestamp → only the message at/after it (inclusive).
	only, err := p.Fetch(context.Background(), time.Unix(tNew, 0))
	if err != nil {
		t.Fatalf("Fetch(tNew): %v", err)
	}
	if len(only) != 1 || only[0].NativeID != "<m10@x.com>" {
		t.Fatalf("Fetch(tNew) = %d items, want 1 (m10)", len(only))
	}
}

func TestFetch_coldStartUsesLookback(t *testing.T) {
	root := writeFixtureRoot(t)
	// A very wide lookback so the 2023 fixture messages fall inside now-lookback.
	provs, err := discoverFromRoot(context.Background(), root, 100*365*24*time.Hour)
	if err != nil {
		t.Fatalf("discoverFromRoot: %v", err)
	}
	p := providerFor(t, provs, acctA)

	items, err := p.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch cold start: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("cold start got %d items, want 2", len(items))
	}

	// Account C has an INBOX but no messages → empty, no error.
	empty, err := providerFor(t, provs, acctC).Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch empty inbox: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty inbox got %d items, want 0", len(empty))
	}
}

func TestFetch_gmailLabelInboxSurfacesAndImportsRead(t *testing.T) {
	// The regression this whole change targets: a Gmail account whose inbox messages
	// live in All Mail and are tied to INBOX only via the labels table. The old
	// `m.mailbox = <inbox>` query returned 0; the label-aware query must surface them.
	root := writeFixtureRoot(t)
	provs, err := discoverFromRoot(context.Background(), root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("discoverFromRoot: %v", err)
	}
	p := providerFor(t, provs, acctG)
	if p.Name() != "owner@g.com" {
		t.Errorf("display name = %q, want owner@g.com (resolved via label-aware recipients)", p.Name())
	}

	items, err := p.Fetch(context.Background(), time.Unix(tOld-1, 0))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// msgs 30 (new, unread) and 31 (old, read) are labeled INBOX; 32 is archived (no
	// label) and must be excluded.
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (archived msg 32 excluded; old query would yield 0)", len(items))
	}
	if items[0].NativeID != "<m30@g.com>" || items[0].Read {
		t.Errorf("item0 = {nativeID:%q read:%v}, want {<m30@g.com> false}", items[0].NativeID, items[0].Read)
	}
	if items[1].NativeID != "<m31@g.com>" || !items[1].Read {
		t.Errorf("item1 = {nativeID:%q read:%v}, want {<m31@g.com> true}", items[1].NativeID, items[1].Read)
	}
}

func TestBody_gmailMessageInStorageMailbox(t *testing.T) {
	// A Gmail inbox message's .emlx lives under [Gmail].mbox/All Mail.mbox, not
	// INBOX.mbox; Body must follow the storage mailbox resolved from the index.
	root := writeFixtureRoot(t)
	writeEmlxIn(t, root, acctG, []string{"[Gmail].mbox", "All Mail.mbox"}, 30,
		"Subject: Hello\r\nContent-Type: text/plain\r\n\r\ngmail inbox body")

	provs, err := discoverFromRoot(context.Background(), root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("discoverFromRoot: %v", err)
	}
	p := providerFor(t, provs, acctG)

	item := &model.Item{ID: "x", NativeID: "<m30@g.com>"}
	if err := p.Body(context.Background(), item); err != nil {
		t.Fatalf("Body: %v", err)
	}
	if !strings.Contains(item.BodyText, "gmail inbox body") {
		t.Errorf("BodyText = %q, want it to contain the All Mail body", item.BodyText)
	}
}

func TestMailboxDirFromURL(t *testing.T) {
	acctDir := filepath.Join("/mail", "ACCT")
	cases := []struct {
		url  string
		want string
	}{
		{"imap://ACCT/INBOX", filepath.Join(acctDir, "INBOX.mbox")},
		{"imap://ACCT/%5BGmail%5D/All%20Mail", filepath.Join(acctDir, "[Gmail].mbox", "All Mail.mbox")},
		{"imap://OTHER/INBOX", ""}, // different account
		{"", ""},                   // empty (rowid: fallback)
		{"imap://ACCT", ""},        // no mailbox path
	}
	for _, c := range cases {
		if got := mailboxDirFromURL(acctDir, "ACCT", c.url); got != c.want {
			t.Errorf("mailboxDirFromURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestEmlxShards(t *testing.T) {
	cases := []struct {
		rowid int64
		want  []string
	}{
		{3, nil},   // < 1000 → no shards (Data/Messages)
		{999, nil}, // boundary
		{1000, []string{"1"}},
		{62473, []string{"2", "6"}},
		{152784, []string{"2", "5", "1"}}, // validated against the live store
		{115344, []string{"5", "1", "1"}},
	}
	for _, c := range cases {
		got := emlxShards(c.rowid)
		if !slices.Equal(got, c.want) {
			t.Errorf("emlxShards(%d) = %v, want %v", c.rowid, got, c.want)
		}
	}
}

func TestStatShardedEmlx(t *testing.T) {
	mailboxDir := t.TempDir()
	const rid = int64(152784)
	// Apple's layout: <mailboxDir>/<store>/Data/2/5/1/Messages/152784.emlx
	target := filepath.Join(mailboxDir, "Store", "Data", "2", "5", "1", "Messages", itoa(rid)+".emlx")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, ok := statShardedEmlx(mailboxDir, rid)
	if !ok || got != target {
		t.Errorf("statShardedEmlx hit = (%q, %v), want (%q, true)", got, ok, target)
	}
	if _, ok := statShardedEmlx(mailboxDir, 999999); ok {
		t.Error("statShardedEmlx for an absent id = found, want miss")
	}
}

func TestFindEmlx_shardedFastPath(t *testing.T) {
	root := writeFixtureRoot(t)
	const rid = int64(152784)
	// Place the .emlx at its sharded All Mail location for account G.
	dir := filepath.Join(root, "V10", acctG, "[Gmail].mbox", "All Mail.mbox", "Store", "Data", "2", "5", "1", "Messages")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rfc := "Subject: x\r\nContent-Type: text/plain\r\n\r\nsharded body"
	emlx := itoa(int64(len(rfc))) + "\n" + rfc + "<?xml?><plist></plist>"
	if err := os.WriteFile(filepath.Join(dir, itoa(rid)+".emlx"), []byte(emlx), 0o644); err != nil {
		t.Fatalf("write emlx: %v", err)
	}

	url := "imap://" + acctG + "/%5BGmail%5D/All%20Mail"
	path, ok, err := findEmlx(root, acctG, url, rid)
	if err != nil {
		t.Fatalf("findEmlx: %v", err)
	}
	if !ok || !strings.Contains(path, filepath.Join("Data", "2", "5", "1", "Messages")) {
		t.Errorf("findEmlx = (%q, %v), want the sharded path", path, ok)
	}
}

func TestInboxMembership(t *testing.T) {
	clause, args := inboxMembership(true, 5)
	if !strings.Contains(clause, "labels") || len(args) != 2 {
		t.Errorf("with labels: clause=%q args=%v, want label-aware OR with 2 args", clause, args)
	}
	clause, args = inboxMembership(false, 5)
	if strings.Contains(clause, "labels") || len(args) != 1 {
		t.Errorf("without labels: clause=%q args=%v, want plain mailbox test with 1 arg", clause, args)
	}
}

func TestHasLabelsTable(t *testing.T) {
	ctx := context.Background()
	// Fixture DB has a labels table.
	root := writeFixtureRoot(t)
	withDB(t, filepath.Join(root, "V10", "MailData", "Envelope Index"), func(db *sql.DB) {
		if !hasLabelsTable(ctx, db) {
			t.Error("hasLabelsTable = false on a DB with labels, want true")
		}
	})
	// A DB without it.
	bare := filepath.Join(t.TempDir(), "bare.sqlite")
	withDB(t, bare, func(db *sql.DB) {
		if _, err := db.Exec(`CREATE TABLE messages (ROWID INTEGER PRIMARY KEY)`); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if hasLabelsTable(ctx, db) {
			t.Error("hasLabelsTable = true on a DB without labels, want false")
		}
	})
}

func withDB(t *testing.T, path string, fn func(*sql.DB)) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	fn(db)
}

func TestFetch_nullReadFlagDefaultsUnread(t *testing.T) {
	// A NULL read flag must not abort the sweep (COALESCE) and must default to unread.
	root := writeFixtureRoot(t)
	withDB(t, filepath.Join(root, "V10", "MailData", "Envelope Index"), func(db *sql.DB) {
		if _, err := db.Exec(`UPDATE messages SET read = NULL WHERE ROWID = 10`); err != nil {
			t.Fatalf("null the read flag: %v", err)
		}
	})

	provs, err := discoverFromRoot(context.Background(), root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("discoverFromRoot: %v", err)
	}
	items, err := providerFor(t, provs, acctA).Fetch(context.Background(), time.Unix(tOld-1, 0))
	if err != nil {
		t.Fatalf("Fetch with NULL read = %v, want nil", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].NativeID != "<m10@x.com>" || items[0].Read {
		t.Errorf("msg 10 with NULL read = {%q read:%v}, want {<m10@x.com> false}", items[0].NativeID, items[0].Read)
	}
}

func TestBody_parsesEmlxByMessageID(t *testing.T) {
	root := writeFixtureRoot(t)
	rfc822 := "From: Alice <alice@x.com>\r\n" +
		"Subject: Hello\r\n" +
		"Mime-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"BOUND\"\r\n\r\n" +
		"--BOUND\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello plain body\r\n" +
		"--BOUND\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Hello html body</p>\r\n" +
		"--BOUND--\r\n"
	writeEmlx(t, root, acctA, 10, rfc822)

	provs, err := discoverFromRoot(context.Background(), root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("discoverFromRoot: %v", err)
	}
	p := providerFor(t, provs, acctA)

	item := &model.Item{ID: "x", NativeID: "<m10@x.com>"}
	if err := p.Body(context.Background(), item); err != nil {
		t.Fatalf("Body: %v", err)
	}
	if !strings.Contains(item.BodyText, "Hello plain body") {
		t.Errorf("BodyText = %q, want it to contain the plain body", item.BodyText)
	}
	if !strings.Contains(item.BodyHTML, "Hello html body") {
		t.Errorf("BodyHTML = %q, want it to contain the html body", item.BodyHTML)
	}
}

func TestBody_rowidFallbackAndMissingFile(t *testing.T) {
	root := writeFixtureRoot(t)
	writeEmlx(t, root, acctA, 11, "Subject: Plain\r\nContent-Type: text/plain\r\n\r\njust text")

	provs, err := discoverFromRoot(context.Background(), root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("discoverFromRoot: %v", err)
	}
	p := providerFor(t, provs, acctA)

	// rowid: fallback native id resolves directly to the file.
	byRowid := &model.Item{ID: "x", NativeID: "rowid:11"}
	if err := p.Body(context.Background(), byRowid); err != nil {
		t.Fatalf("Body(rowid): %v", err)
	}
	if !strings.Contains(byRowid.BodyText, "just text") {
		t.Errorf("BodyText = %q, want 'just text'", byRowid.BodyText)
	}

	// A message whose .emlx is absent leaves the body empty and returns no error.
	missing := &model.Item{ID: "y", NativeID: "<m10@x.com>"}
	if err := p.Body(context.Background(), missing); err != nil {
		t.Fatalf("Body(missing): %v", err)
	}
	if missing.BodyText != "" || missing.BodyHTML != "" {
		t.Errorf("missing body = %q / %q, want empty", missing.BodyText, missing.BodyHTML)
	}
}

func TestDiscoverFromRoot_noMailData(t *testing.T) {
	// An empty root (no V<n> directory) means Apple Mail is not set up: no providers,
	// no error.
	provs, err := discoverFromRoot(context.Background(), t.TempDir(), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("discoverFromRoot empty root: %v", err)
	}
	if provs != nil {
		t.Fatalf("got %d providers, want nil for an empty root", len(provs))
	}
}
