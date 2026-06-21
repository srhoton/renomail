package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	"github.com/srhoton/renomail/internal/config"
	"github.com/srhoton/renomail/internal/model"
)

func TestProviderIdentity(t *testing.T) {
	p := newWithService("me@example.com", nil, 30*24*time.Hour)
	if got := p.ID(); got != "gmail:me@example.com" {
		t.Errorf("ID() = %q", got)
	}
	if got := p.Name(); got != "me@example.com" {
		t.Errorf("Name() = %q", got)
	}
	if got := p.Kind(); got != model.KindEmail {
		t.Errorf("Kind() = %q", got)
	}
}

func TestToItem_mapsHeadersAndFields(t *testing.T) {
	p := newWithService("me@example.com", nil, 0)
	fetched := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	msg := &gmailapi.Message{
		Id:      "abc123",
		Snippet: "Preview &amp; text",
		Payload: &gmailapi.MessagePart{Headers: []*gmailapi.MessagePartHeader{
			{Name: "From", Value: "Alice <alice@example.com>"},
			{Name: "Subject", Value: "Hello"},
			{Name: "Date", Value: "Thu, 20 Jun 2024 21:00:00 +0000"},
		}},
	}

	it := p.toItem(msg, fetched)
	if it.ID != model.StableID("gmail:me@example.com", "abc123") {
		t.Errorf("ID = %q, want StableID(source, msgID)", it.ID)
	}
	if it.NativeID != "abc123" {
		t.Errorf("NativeID = %q", it.NativeID)
	}
	if it.Kind != model.KindEmail || it.SourceID != "gmail:me@example.com" || it.SourceName != "me@example.com" {
		t.Errorf("source fields wrong: %+v", it)
	}
	if it.Author != "Alice <alice@example.com>" || it.Title != "Hello" {
		t.Errorf("header mapping wrong: author=%q title=%q", it.Author, it.Title)
	}
	if it.Snippet != "Preview & text" {
		t.Errorf("snippet not HTML-unescaped: %q", it.Snippet)
	}
	if !strings.Contains(it.URL, "abc123") || !strings.Contains(it.URL, "me@example.com") {
		t.Errorf("deep-link URL wrong: %q", it.URL)
	}
	if it.Published.IsZero() || !it.Fetched.Equal(fetched) {
		t.Errorf("times wrong: published=%v fetched=%v", it.Published, it.Fetched)
	}
	if it.BodyHTML != "" || it.BodyText != "" {
		t.Error("body should be empty in metadata mapping")
	}
}

func TestToItem_nilPayload_noPanic(t *testing.T) {
	p := newWithService("me@example.com", nil, 0)
	it := p.toItem(&gmailapi.Message{Id: "x", InternalDate: 1718928000000}, time.Now())
	if it.Author != "" || it.Title != "" {
		t.Errorf("nil payload should yield empty headers: %+v", it)
	}
	if it.Published.IsZero() {
		t.Error("should fall back to InternalDate")
	}
}

func TestHeaders_caseInsensitiveFirstWins(t *testing.T) {
	h := headers([]*gmailapi.MessagePartHeader{
		{Name: "From", Value: "first"},
		{Name: "FROM", Value: "second"},
		nil, // nil entry must be skipped, not panic
		{Name: "Subject", Value: "subj"},
	})
	if h["from"] != "first" {
		t.Errorf("from = %q, want first-wins", h["from"])
	}
	if h["subject"] != "subj" {
		t.Errorf("subject = %q", h["subject"])
	}
}

func TestParseDate(t *testing.T) {
	internal := int64(1718928000000) // 2024-06-21T00:00:00Z
	tests := []struct {
		name     string
		header   string
		internal int64
		wantZero bool
		wantYear int
	}{
		{"rfc5322 header", "Thu, 20 Jun 2024 21:00:00 +0000", internal, false, 2024},
		{"unparseable header falls back to internal", "not a date", internal, false, 2024},
		{"no header uses internal", "", internal, false, 2024},
		{"nothing yields zero", "", 0, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDate(tt.header, tt.internal)
			if got.IsZero() != tt.wantZero {
				t.Fatalf("IsZero = %v, want %v (got %v)", got.IsZero(), tt.wantZero, got)
			}
			if !tt.wantZero && got.Year() != tt.wantYear {
				t.Errorf("year = %d, want %d", got.Year(), tt.wantYear)
			}
			if !tt.wantZero && got.Location() != time.UTC {
				t.Errorf("not UTC: %v", got.Location())
			}
		})
	}
}

func TestDays(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want int
	}{
		{0, 1},
		{-time.Hour, 1},
		{time.Hour, 1},
		{24 * time.Hour, 1},
		{25 * time.Hour, 2},
		{30 * 24 * time.Hour, 30},
	}
	for _, tt := range tests {
		if got := days(tt.in); got != tt.want {
			t.Errorf("days(%v) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestQuery(t *testing.T) {
	p := newWithService("me@example.com", nil, 30*24*time.Hour)
	if got := p.query(time.Time{}); got != "in:inbox newer_than:30d" {
		t.Errorf("cold-start query = %q", got)
	}
	since := time.Unix(1700000000, 0)
	if got := p.query(since); got != "in:inbox after:1700000000" {
		t.Errorf("incremental query = %q", got)
	}
}

func TestNew_missingToken_returnsErrNotAuthorized(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(path.Join(dir, "credentials.json"), []byte(minimalCredentials), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	paths := config.Paths{ConfigDir: dir, Credentials: path.Join(dir, "credentials.json")}

	_, err := New(context.Background(), paths, "nobody@example.com", time.Hour)
	if !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("err = %v, want ErrNotAuthorized", err)
	}
}

func TestNew_withToken_buildsProvider(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(credPath, []byte(minimalCredentials), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	paths := config.Paths{ConfigDir: dir, Credentials: credPath}
	tok := `{"access_token":"at","refresh_token":"rt","token_type":"Bearer"}`
	if err := os.WriteFile(paths.TokenFile("me@example.com"), []byte(tok), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	p, err := New(context.Background(), paths, "me@example.com", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.ID() != "gmail:me@example.com" || p.lookback != 30*24*time.Hour {
		t.Errorf("provider misconfigured: id=%q lookback=%v", p.ID(), p.lookback)
	}
}

func TestNew_missingCredentials_returnsError(t *testing.T) {
	paths := config.Paths{ConfigDir: t.TempDir(), Credentials: path.Join(t.TempDir(), "absent.json")}
	if _, err := New(context.Background(), paths, "x@example.com", time.Hour); err == nil {
		t.Error("New with missing credentials = nil error, want read error")
	}
}

// gmailTestService points a real *gmail.Service at an httptest server so Fetch
// and Body exercise the listing/get code paths offline.
func gmailTestService(t *testing.T, h http.HandlerFunc) *gmailapi.Service {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	svc, err := gmailapi.NewService(context.Background(),
		option.WithoutAuthentication(), option.WithEndpoint(srv.URL+"/"))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestFetch_listsAndMapsMessages(t *testing.T) {
	svc := gmailTestService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_ = json.NewEncoder(w).Encode(&gmailapi.ListMessagesResponse{
				Messages: []*gmailapi.Message{{Id: "m1"}, {Id: "m2"}},
			})
		case strings.Contains(r.URL.Path, "/messages/"):
			id := path.Base(r.URL.Path)
			_ = json.NewEncoder(w).Encode(&gmailapi.Message{
				Id: id, Snippet: "snippet " + id, InternalDate: 1718928000000,
				Payload: &gmailapi.MessagePart{Headers: []*gmailapi.MessagePartHeader{
					{Name: "From", Value: "a@example.com"},
					{Name: "Subject", Value: "Subject " + id},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	})

	p := newWithService("me@example.com", svc, 30*24*time.Hour)
	items, err := p.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].Title != "Subject m1" || items[1].Title != "Subject m2" {
		t.Errorf("titles wrong: %q, %q", items[0].Title, items[1].Title)
	}
	if items[0].NativeID != "m1" {
		t.Errorf("native id = %q", items[0].NativeID)
	}
}

// TestFetch_concurrentGets_orderedAndBounded verifies the two-phase Fetch: the
// per-message metadata Gets run concurrently (peak in-flight bounded by
// fetchConcurrency) yet the returned items keep the list order regardless of the
// order the concurrent Gets complete.
func TestFetch_concurrentGets_orderedAndBounded(t *testing.T) {
	const n = 25
	var inflight, peak atomic.Int32

	msgs := make([]*gmailapi.Message, 0, n)
	for i := range n {
		msgs = append(msgs, &gmailapi.Message{Id: "m" + strconv.Itoa(i)})
	}

	svc := gmailTestService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_ = json.NewEncoder(w).Encode(&gmailapi.ListMessagesResponse{Messages: msgs})
		case strings.Contains(r.URL.Path, "/messages/"):
			cur := inflight.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond) // widen the concurrency window
			inflight.Add(-1)
			id := path.Base(r.URL.Path)
			_ = json.NewEncoder(w).Encode(&gmailapi.Message{
				Id: id,
				Payload: &gmailapi.MessagePart{Headers: []*gmailapi.MessagePartHeader{
					{Name: "Subject", Value: "Subject " + id},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	})

	p := newWithService("me@example.com", svc, time.Hour)
	items, err := p.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(items) != n {
		t.Fatalf("got %d items, want %d", len(items), n)
	}
	for i, it := range items {
		want := "Subject m" + strconv.Itoa(i)
		if it.Title != want {
			t.Fatalf("items[%d].Title = %q, want %q (order not preserved)", i, it.Title, want)
		}
	}
	if got := peak.Load(); got > fetchConcurrency {
		t.Errorf("peak concurrent Gets = %d, want <= fetchConcurrency (%d)", got, fetchConcurrency)
	}
	if peak.Load() < 2 {
		t.Errorf("peak concurrent Gets = %d, want > 1 (Gets should overlap)", peak.Load())
	}
}

func TestFetch_listError_propagates(t *testing.T) {
	svc := gmailTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	p := newWithService("me@example.com", svc, time.Hour)
	if _, err := p.Fetch(context.Background(), time.Time{}); err == nil {
		t.Error("Fetch error = nil, want propagated list error")
	}
}

// TestFetch_partialGetFailure_returnsFetchedAndError verifies resilience: when one
// per-message Get fails, Fetch still returns the messages it did fetch (in list
// order) alongside the error, rather than discarding the whole batch.
func TestFetch_partialGetFailure_returnsFetchedAndError(t *testing.T) {
	svc := gmailTestService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_ = json.NewEncoder(w).Encode(&gmailapi.ListMessagesResponse{
				Messages: []*gmailapi.Message{{Id: "m1"}, {Id: "m2"}, {Id: "m3"}},
			})
		case strings.Contains(r.URL.Path, "/messages/"):
			id := path.Base(r.URL.Path)
			if id == "m2" {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(&gmailapi.Message{
				Id: id,
				Payload: &gmailapi.MessagePart{Headers: []*gmailapi.MessagePartHeader{
					{Name: "Subject", Value: "Subject " + id},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	})

	p := newWithService("me@example.com", svc, time.Hour)
	items, err := p.Fetch(context.Background(), time.Time{})
	if err == nil {
		t.Fatal("Fetch error = nil, want the failed Get surfaced")
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want the 2 that succeeded (m1, m3)", len(items))
	}
	if items[0].Title != "Subject m1" || items[1].Title != "Subject m3" {
		t.Errorf("partial result order wrong: %q, %q", items[0].Title, items[1].Title)
	}
}

func TestBody_loadsAndDecodesMIME(t *testing.T) {
	htmlData := base64.RawURLEncoding.EncodeToString([]byte("<p>body html</p>"))
	textData := base64.RawURLEncoding.EncodeToString([]byte("body text"))
	svc := gmailTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "full" {
			t.Errorf("Body should request full format, got %q", r.URL.Query().Get("format"))
		}
		_ = json.NewEncoder(w).Encode(&gmailapi.Message{
			Id: path.Base(r.URL.Path),
			Payload: &gmailapi.MessagePart{MimeType: "multipart/alternative", Parts: []*gmailapi.MessagePart{
				{MimeType: "text/plain", Body: &gmailapi.MessagePartBody{Data: textData}},
				{MimeType: "text/html", Body: &gmailapi.MessagePartBody{Data: htmlData}},
			}},
		})
	})

	p := newWithService("me@example.com", svc, time.Hour)
	it := &model.Item{ID: "id1", NativeID: "m1"}
	if err := p.Body(context.Background(), it); err != nil {
		t.Fatalf("Body: %v", err)
	}
	if it.BodyHTML != "<p>body html</p>" || it.BodyText != "body text" {
		t.Errorf("body decode = (%q, %q)", it.BodyHTML, it.BodyText)
	}
}

func TestBody_noNativeID_returnsError(t *testing.T) {
	p := newWithService("me@example.com", nil, time.Hour)
	if err := p.Body(context.Background(), &model.Item{ID: "id1"}); err == nil {
		t.Error("Body without NativeID = nil error, want error")
	}
}

func TestBody_getError_propagates(t *testing.T) {
	svc := gmailTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	})
	p := newWithService("me@example.com", svc, time.Hour)
	if err := p.Body(context.Background(), &model.Item{NativeID: "m1"}); err == nil {
		t.Error("Body get error = nil, want propagated error")
	}
}
