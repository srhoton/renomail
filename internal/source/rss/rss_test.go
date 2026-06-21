package rss

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/srhoton/renomail/internal/model"
)

func testProvider(url string, src model.Source) *Provider {
	return New(src, url, &http.Client{Timeout: 5 * time.Second})
}

func TestFetch_rssFixture_mapsEntries(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "sample_rss.xml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	src := model.Source{ID: "rss:test", Name: "My Feed", Kind: model.KindRSS}
	items, err := testProvider(srv.URL, src).Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	first := items[0]
	if first.Title != "First Post" {
		t.Errorf("Title = %q, want %q", first.Title, "First Post")
	}
	if first.URL != "https://example.com/posts/1" {
		t.Errorf("URL = %q", first.URL)
	}
	if first.NativeID != "https://example.com/posts/1" {
		t.Errorf("NativeID = %q", first.NativeID)
	}
	if first.ID != model.StableID(src.ID, first.NativeID) {
		t.Errorf("ID = %q, want StableID(%q,%q)", first.ID, src.ID, first.NativeID)
	}
	if first.Kind != model.KindRSS {
		t.Errorf("Kind = %q, want %q", first.Kind, model.KindRSS)
	}
	if first.SourceName != "My Feed" {
		t.Errorf("SourceName = %q, want %q", first.SourceName, "My Feed")
	}
	if !strings.Contains(first.BodyHTML, "<b>first</b>") {
		t.Errorf("BodyHTML lost markup: %q", first.BodyHTML)
	}
	if strings.Contains(first.BodyText, "<") {
		t.Errorf("BodyText still has markup: %q", first.BodyText)
	}
	if !strings.Contains(first.Snippet, "welcome") {
		t.Errorf("Snippet = %q", first.Snippet)
	}
	if first.Published.IsZero() {
		t.Error("Published is zero")
	}
	if first.Fetched.IsZero() {
		t.Error("Fetched is zero")
	}
}

func TestFetch_atomFixture_mapsEntries(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "sample_atom.xml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	src := model.Source{ID: "rss:atom", Name: "Atom Feed", Kind: model.KindRSS}
	items, err := testProvider(srv.URL, src).Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	it := items[0]
	if it.Title != "Atom Entry One" {
		t.Errorf("Title = %q", it.Title)
	}
	if it.Author != "Bob" {
		t.Errorf("Author = %q, want %q", it.Author, "Bob")
	}
	if it.NativeID != "urn:uuid:atom-entry-1" {
		t.Errorf("NativeID = %q (should fall back to GUID/id)", it.NativeID)
	}
	if !strings.Contains(it.BodyText, "content") || strings.Contains(it.BodyText, "<i>") {
		t.Errorf("BodyText = %q", it.BodyText)
	}
}

func TestFetch_notModified_returnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(srv.Close)

	src := model.Source{ID: "rss:x", Name: "X", Kind: model.KindRSS, ETag: `"abc"`}
	items, err := testProvider(srv.URL, src).Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if items != nil {
		t.Errorf("items = %v, want nil on 304", items)
	}
}

func TestFetch_conditionalGet_sendsAndCapturesValidators(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "sample_rss.xml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var gotINM, gotIMS string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotINM = r.Header.Get("If-None-Match")
		gotIMS = r.Header.Get("If-Modified-Since")
		w.Header().Set("ETag", `"v2"`)
		w.Header().Set("Last-Modified", "Wed, 04 Jan 2006 00:00:00 GMT")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	src := model.Source{
		ID: "rss:cond", Name: "Cond", Kind: model.KindRSS,
		ETag: `"v1"`, LastModified: "Tue, 03 Jan 2006 00:00:00 GMT",
	}
	p := testProvider(srv.URL, src)
	if _, err := p.Fetch(context.Background(), time.Time{}); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	if gotINM != `"v1"` {
		t.Errorf("If-None-Match sent = %q, want %q", gotINM, `"v1"`)
	}
	if gotIMS != "Tue, 03 Jan 2006 00:00:00 GMT" {
		t.Errorf("If-Modified-Since sent = %q", gotIMS)
	}
	if st := p.SourceState(); st.ETag != `"v2"` || st.LastModified != "Wed, 04 Jan 2006 00:00:00 GMT" {
		t.Errorf("captured validators = %q / %q, want %q / %q",
			st.ETag, st.LastModified, `"v2"`, "Wed, 04 Jan 2006 00:00:00 GMT")
	}
}

func TestFetch_non200_returnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	src := model.Source{ID: "rss:err", Name: "Err", Kind: model.KindRSS}
	if _, err := testProvider(srv.URL, src).Fetch(context.Background(), time.Time{}); err == nil {
		t.Fatal("Fetch() error = nil, want error on 500")
	}
}

func TestFetch_concurrent_noRace(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "sample_rss.xml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"v"`)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	// One Provider fetched from many goroutines must not race on its validators
	// (run under `go test -race`). This is the access pattern the step 07 sync
	// engine relies on.
	p := testProvider(srv.URL, model.Source{ID: "rss:c", Name: "C", Kind: model.KindRSS})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Fetch(context.Background(), time.Time{}); err != nil {
				t.Errorf("Fetch() error = %v", err)
			}
			_ = p.SourceState()
		}()
	}
	wg.Wait()
}

func TestProvider_accessors(t *testing.T) {
	src := model.Source{ID: "rss:a", Name: "Accessor Feed", Kind: model.KindRSS}
	p := New(src, "https://example.com/a.xml", nil) // nil client => default timeout
	if p.ID() != src.ID {
		t.Errorf("ID() = %q, want %q", p.ID(), src.ID)
	}
	if p.Name() != src.Name {
		t.Errorf("Name() = %q, want %q", p.Name(), src.Name)
	}
	if p.Kind() != model.KindRSS {
		t.Errorf("Kind() = %q, want %q", p.Kind(), model.KindRSS)
	}
	if err := p.Body(context.Background(), &model.Item{}); err != nil {
		t.Errorf("Body() error = %v, want nil", err)
	}
}

func TestToItem_publishedFallback(t *testing.T) {
	updated := time.Date(2020, 5, 1, 12, 0, 0, 0, time.UTC)
	now := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	p := New(model.Source{ID: "rss:f", Name: "F", Kind: model.KindRSS}, "u", nil)

	// No PublishedParsed but UpdatedParsed present -> use Updated.
	it := p.toItem(&gofeed.Feed{Title: "F"}, &gofeed.Item{Title: "t", UpdatedParsed: &updated}, now)
	if !it.Published.Equal(updated) {
		t.Errorf("Published = %v, want updated %v", it.Published, updated)
	}

	// Neither present -> fall back to now.
	it2 := p.toItem(&gofeed.Feed{Title: "F"}, &gofeed.Item{Title: "t"}, now)
	if !it2.Published.Equal(now) {
		t.Errorf("Published = %v, want now %v", it2.Published, now)
	}
}

func TestPlainText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain unchanged", "hello world", "hello world"},
		{"fast path collapses", "no   markup\nhere", "no markup here"},
		{"strips tags", "<p>hello <b>world</b></p>", "hello world"},
		{"unescapes entities", "a &amp; b &lt;c&gt;", "a & b <c>"},
		{"collapses whitespace", "<p>a</p>\n\n  <p>b</p>", "a b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := plainText(tt.in); got != tt.want {
				t.Errorf("plainText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"shorter than limit", "hello world", 50, "hello world"},
		{"exact length", "abcde", 5, "abcde"},
		{"truncates", "abcdefghij", 5, "abcde…"},
		{"rune-safe truncate", "héllo wörld", 4, "héll…"},
		{"trims trailing space at cut", "ab cdef", 3, "ab…"},
		{"zero length", "anything", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateRunes(tt.in, tt.n); got != tt.want {
				t.Errorf("truncateRunes(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}
