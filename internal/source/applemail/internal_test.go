package applemail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/srhoton/renomail/internal/model"
)

func TestAccountFromURL(t *testing.T) {
	cases := []struct {
		url    string
		want   string
		wantOK bool
	}{
		{"imap://UUID-1/INBOX", "UUID-1", true},
		{"ews://UUID-2/Inbox/Sub", "UUID-2", true},
		{"imap://UUID-3/", "UUID-3", true},
		{"no-scheme/INBOX", "", false},
		{"imap://UUID-only", "", false},
	}
	for _, c := range cases {
		got, ok := accountFromURL(c.url)
		if got != c.want || ok != c.wantOK {
			t.Errorf("accountFromURL(%q) = (%q, %v), want (%q, %v)", c.url, got, ok, c.want, c.wantOK)
		}
	}
}

func TestMessageURL(t *testing.T) {
	cases := map[string]string{
		"<abc@d.com>": "message://%3Cabc@d.com%3E",
		"abc@d.com":   "message://%3Cabc@d.com%3E",
		"  <x@y>  ":   "message://%3Cx@y%3E",
		"":            "",
		"<>":          "",
	}
	for in, want := range cases {
		if got := messageURL(in); got != want {
			t.Errorf("messageURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatSenderAndSubject(t *testing.T) {
	if got := formatSender("Alice", "a@x.com"); got != "Alice <a@x.com>" {
		t.Errorf("formatSender both = %q", got)
	}
	if got := formatSender("", "a@x.com"); got != "a@x.com" {
		t.Errorf("formatSender addr-only = %q", got)
	}
	if got := formatSender("Alice", ""); got != "Alice" {
		t.Errorf("formatSender name-only = %q", got)
	}
	if got := formatSubject("Re:", "Hi"); got != "Re: Hi" {
		t.Errorf("formatSubject prefixed = %q", got)
	}
	if got := formatSubject("", "Hi"); got != "Hi" {
		t.Errorf("formatSubject plain = %q", got)
	}
}

func TestParseVersionDir(t *testing.T) {
	cases := []struct {
		name   string
		want   int
		wantOK bool
	}{
		{"V10", 10, true},
		{"V2", 2, true},
		{"v7", 7, true},
		{"MailData", 0, false},
		{"V", 0, false},
		{"Vx", 0, false},
	}
	for _, c := range cases {
		got, ok := parseVersionDir(c.name)
		if got != c.want || ok != c.wantOK {
			t.Errorf("parseVersionDir(%q) = (%d, %v), want (%d, %v)", c.name, got, ok, c.want, c.wantOK)
		}
	}
}

func TestDiscoverFromRoot_picksHighestVersionAndIgnoresJunk(t *testing.T) {
	root := writeFixtureRoot(t) // creates V10/MailData/Envelope Index
	// An older, empty version dir and a non-version dir must be ignored.
	if err := os.MkdirAll(filepath.Join(root, "V2", "MailData"), 0o755); err != nil {
		t.Fatalf("mkdir V2: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "NotAVersion"), 0o755); err != nil {
		t.Fatalf("mkdir junk: %v", err)
	}

	provs, err := discoverFromRoot(context.Background(), root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("discoverFromRoot: %v", err)
	}
	if len(provs) != 3 {
		t.Fatalf("got %d providers, want 3 (V10 selected)", len(provs))
	}
}

func TestDiscoverFromRoot_noAccessIsSentinel(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses file permissions")
	}
	root := writeFixtureRoot(t)
	idx := filepath.Join(root, "V10", "MailData", "Envelope Index")
	if err := os.Chmod(idx, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(idx, 0o644) })

	// Confirm the unreadable state actually denies the owner (some environments
	// ignore the bits); otherwise the assertion would be meaningless.
	if f, err := os.Open(idx); err == nil {
		_ = f.Close()
		t.Skip("filesystem does not enforce 0000 for owner")
	}

	if _, err := discoverFromRoot(context.Background(), root, 30*24*time.Hour); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("discoverFromRoot with unreadable index = %v, want ErrNoAccess", err)
	}
}

func TestFetch_accountWithoutInbox(t *testing.T) {
	root := writeFixtureRoot(t)
	p := newWithRoot(root, Account{ID: "NO-SUCH-ACCT", Display: "ghost"}, 30*24*time.Hour)
	items, err := p.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d items, want 0 for an account with no INBOX", len(items))
	}
}

func TestBody_noMailDataIsNoOp(t *testing.T) {
	p := newWithRoot(t.TempDir(), Account{ID: acctA, Display: "x"}, 30*24*time.Hour)
	item := &model.Item{ID: "z", NativeID: "<whatever@x>"}
	if err := p.Body(context.Background(), item); err != nil {
		t.Fatalf("Body with no mail data = %v, want nil", err)
	}
	if item.BodyText != "" || item.BodyHTML != "" {
		t.Errorf("body filled = %q / %q, want empty", item.BodyText, item.BodyHTML)
	}
}

func TestNewWithRoot_clampsLookback(t *testing.T) {
	p := newWithRoot("/x", Account{ID: "a"}, 0)
	if p.lookback != defaultLookback {
		t.Errorf("lookback = %v, want clamp to %v", p.lookback, defaultLookback)
	}
}

func TestIndexSnapshot_reusesUntilSourceChanges(t *testing.T) {
	root := writeFixtureRoot(t)
	CloseCache() // start from a clean global cache regardless of test order
	t.Cleanup(CloseCache)

	s1, err := indexCache.acquire(root)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	indexCache.release(s1)

	// Unchanged source → the same snapshot (and its DB/temp copy) is reused.
	s2, err := indexCache.acquire(root)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	indexCache.release(s2)
	if s2 != s1 {
		t.Fatal("expected snapshot reuse, got a fresh snapshot for an unchanged source")
	}

	// Bump the source index's mtime → change key shifts → fresh snapshot.
	idx := filepath.Join(root, "V10", "MailData", "Envelope Index")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(idx, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	s3, err := indexCache.acquire(root)
	if err != nil {
		t.Fatalf("acquire 3: %v", err)
	}
	indexCache.release(s3)
	if s3 == s1 {
		t.Error("expected a fresh snapshot after the source changed, got the cached one")
	}
}

func TestIndexSnapshot_refreshDoesNotCloseInFlightReader(t *testing.T) {
	root := writeFixtureRoot(t)
	CloseCache()
	t.Cleanup(CloseCache)

	// Hold a reader of the first snapshot.
	s1, err := indexCache.acquire(root)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}

	// Force a refresh while s1 is still held: it must be retired but NOT closed,
	// so s1.db stays usable until release.
	idx := filepath.Join(root, "V10", "MailData", "Envelope Index")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(idx, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	s2, err := indexCache.acquire(root)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if s2 == s1 {
		t.Fatal("expected a new snapshot after the source changed")
	}

	// s1 was superseded but is still referenced — its DB must still answer queries.
	if err := s1.db.PingContext(context.Background()); err != nil {
		t.Errorf("in-flight snapshot DB closed under us: %v", err)
	}
	indexCache.release(s1) // now refs==0 && retired → closed
	indexCache.release(s2)
}

func TestIndexSnapshot_concurrentReadersAndRefreshNoUseAfterClose(t *testing.T) {
	// Reproduces the multi-account-sweep scenario: many readers acquire/query/release
	// the shared snapshot while the source index churns, forcing refreshes. Under
	// -race this would flag a snapshot closed out from under an in-flight reader.
	root := writeFixtureRoot(t)
	CloseCache()
	t.Cleanup(CloseCache)
	idx := filepath.Join(root, "V10", "MailData", "Envelope Index")

	var wg sync.WaitGroup

	// Refresher: bump the index mtime repeatedly so acquire keeps re-copying.
	wg.Add(1)
	go func() {
		defer wg.Done()
		base := time.Now()
		for i := 0; i < 80; i++ {
			ts := base.Add(time.Duration(i) * time.Second)
			_ = os.Chtimes(idx, ts, ts)
			time.Sleep(time.Millisecond)
		}
	}()

	// Readers: acquire, run a query on the shared DB, release.
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				s, err := indexCache.acquire(root)
				if err != nil {
					t.Errorf("acquire: %v", err)
					return
				}
				if err := s.db.PingContext(context.Background()); err != nil {
					t.Errorf("query on a held snapshot failed (closed under us?): %v", err)
				}
				indexCache.release(s)
			}
		}()
	}
	wg.Wait()
}

func TestDiscover_usesMailRootSeam(t *testing.T) {
	root := writeFixtureRoot(t)
	orig := mailRoot
	mailRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { mailRoot = orig })

	provs, err := Discover(context.Background(), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(provs) != 3 {
		t.Fatalf("got %d providers, want 3", len(provs))
	}
}
