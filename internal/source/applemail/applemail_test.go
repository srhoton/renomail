package applemail

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
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
		`CREATE TABLE mailboxes (ROWID INTEGER PRIMARY KEY, url TEXT)`,
		`CREATE TABLE addresses (ROWID INTEGER PRIMARY KEY, address TEXT, comment TEXT)`,
		`CREATE TABLE subjects (ROWID INTEGER PRIMARY KEY, subject TEXT)`,
		`CREATE TABLE summaries (ROWID INTEGER PRIMARY KEY, summary TEXT)`,
		`CREATE TABLE message_global_data (ROWID INTEGER PRIMARY KEY, message_id_header TEXT)`,
		`CREATE TABLE recipients (ROWID INTEGER PRIMARY KEY, message INTEGER, address INTEGER, type INTEGER)`,
		`CREATE TABLE messages (ROWID INTEGER PRIMARY KEY, global_message_id INTEGER, sender INTEGER,
		   subject_prefix TEXT, subject INTEGER, summary INTEGER, date_received INTEGER, mailbox INTEGER, deleted INTEGER)`,

		// Mailboxes: an INBOX per account; a Sent mailbox only for A.
		`INSERT INTO mailboxes (ROWID, url) VALUES
		   (1, 'imap://` + acctA + `/INBOX'),
		   (2, 'imap://` + acctA + `/Sent Messages'),
		   (3, 'imap://` + acctB + `/INBOX'),
		   (4, 'imap://` + acctC + `/INBOX')`,

		`INSERT INTO addresses (ROWID, address, comment) VALUES
		   (1, 'alice@x.com', 'Alice'),
		   (2, 'bob@y.com', ''),
		   (3, 'me@a.example', ''),
		   (4, 'me@b.example', '')`,

		`INSERT INTO subjects (ROWID, subject) VALUES (1, 'Hello'), (2, 'Meeting')`,
		`INSERT INTO summaries (ROWID, summary) VALUES (1, 'hi there'), (2, 'agenda')`,
		`INSERT INTO message_global_data (ROWID, message_id_header) VALUES
		   (100, '<m10@x.com>'), (101, '<m11@y.com>'), (102, '<m12@b.example>')`,

		// A/INBOX: msg 10 (new), msg 11 (old, "Re:"), msg 13 (deleted → excluded).
		`INSERT INTO messages (ROWID, global_message_id, sender, subject_prefix, subject, summary, date_received, mailbox, deleted) VALUES
		   (10, 100, 1, '',    1, 1, ` + itoa(tNew) + `, 1, 0),
		   (11, 101, 2, 'Re:', 2, 2, ` + itoa(tOld) + `, 1, 0),
		   (13, 0,   1, '',    1, 1, ` + itoa(tNew) + `, 1, 1)`,
		// A/Sent: msg 20 from me@a.example (owner address).
		`INSERT INTO messages (ROWID, global_message_id, sender, subject_prefix, subject, summary, date_received, mailbox, deleted) VALUES
		   (20, 0, 3, '', 1, 1, 1690000000, 2, 0)`,
		// B/INBOX: msg 12, with a To recipient me@b.example.
		`INSERT INTO messages (ROWID, global_message_id, sender, subject_prefix, subject, summary, date_received, mailbox, deleted) VALUES
		   (12, 102, 1, '', 1, 1, ` + itoa(tOld) + `, 3, 0)`,
		`INSERT INTO recipients (ROWID, message, address, type) VALUES (1, 12, 4, 0)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed fixture (%.40s…): %v", s, err)
		}
	}
	return root
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// writeEmlx writes a fixture .emlx file for a message row id under the account's
// INBOX mailbox, mirroring the real on-disk layout.
func writeEmlx(t *testing.T, root, acctID string, rowid int64, rfc822 string) {
	t.Helper()
	dir := filepath.Join(root, "V10", acctID, "INBOX.mbox", "Store", "Data", "Messages")
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
	if len(provs) != 3 {
		t.Fatalf("got %d providers, want 3", len(provs))
	}
	// Sorted by display: "Apple Mail (CCCCCCCC)" < "me@a.example" < "me@b.example".
	wantNames := []string{"Apple Mail (CCCCCCCC)", "me@a.example", "me@b.example"}
	wantIDs := []string{"applemail:" + acctC, "applemail:" + acctA, "applemail:" + acctB}
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
