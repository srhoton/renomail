package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/srhoton/renomail/internal/model"
)

func at(unix int64) time.Time { return time.Unix(unix, 0).UTC() }

func emailItem(source, title, author, url string, published int64) model.Item {
	return model.Item{
		Kind: model.KindEmail, SourceName: source, Title: title,
		Author: author, URL: url, Published: at(published),
	}
}

func rssItem(source, title, url string, published int64) model.Item {
	return model.Item{
		Kind: model.KindRSS, SourceName: source, Title: title,
		URL: url, Published: at(published),
	}
}

// captureServer starts an httptest server that records the first request's method,
// content-type, and decoded payload, replying with the given status.
func captureServer(t *testing.T, status int) (*httptest.Server, *slackPayload, *string, *string) {
	t.Helper()
	var (
		payload     slackPayload
		method      string
		contentType string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		contentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &payload)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &payload, &method, &contentType
}

// allText concatenates the payload's fallback text and every block's mrkdwn so a
// test can assert on the rendered content without depending on block boundaries.
func allText(p *slackPayload) string {
	var b strings.Builder
	b.WriteString(p.Text)
	for _, blk := range p.Blocks {
		if blk.Text != nil {
			b.WriteString("\n")
			b.WriteString(blk.Text.Text)
		}
	}
	return b.String()
}

func TestSlackNotify_postsGroupedRichPayload(t *testing.T) {
	srv, payload, method, contentType := captureServer(t, http.StatusOK)

	items := []model.Item{
		rssItem("Hacker News", "Show HN: a TUI mail reader", "https://news.example/1", 200),
		rssItem("Hacker News", "Rust eats the world", "https://news.example/2", 100),
		emailItem("Personal Gmail", "Re: lunch Friday", "Alex", "https://mail.example/3", 150),
	}
	if err := NewSlack(srv.URL, 10).Notify(context.Background(), items); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}

	if *method != http.MethodPost {
		t.Errorf("method = %q, want POST", *method)
	}
	if !strings.HasPrefix(*contentType, "application/json") {
		t.Errorf("content-type = %q, want application/json", *contentType)
	}

	text := allText(payload)
	for _, want := range []string{
		"3 new across 2",                // summary count + source count
		"Hacker News", "Personal Gmail", // source group headers
		"Show HN: a TUI mail reader", // titles
		"Rust eats the world",
		"Re: lunch Friday",
		"from Alex",                // sender for the email item
		"<https://mail.example/3|", // linked title
	} {
		if !strings.Contains(text, want) {
			t.Errorf("payload missing %q\n---\n%s", want, text)
		}
	}
}

func TestSlackNotify_capsItemsWithMore(t *testing.T) {
	srv, payload, _, _ := captureServer(t, http.StatusOK)

	items := make([]model.Item, 5)
	for i := range items {
		items[i] = rssItem("Feed", fmt.Sprintf("title-%d", i), fmt.Sprintf("https://e/%d", i), int64(i))
	}
	if err := NewSlack(srv.URL, 2).Notify(context.Background(), items); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}

	text := allText(payload)
	if !strings.Contains(text, "5 new") {
		t.Errorf("summary should report the true total of 5; got\n%s", text)
	}
	if !strings.Contains(text, "and 3 more") {
		t.Errorf("expected a '…and 3 more' line for the 3 items past the cap of 2; got\n%s", text)
	}
}

func TestSlackNotify_urlWithDelimiters_fallsBackToPlainTitle(t *testing.T) {
	srv, payload, _, _ := captureServer(t, http.StatusOK)

	// A URL containing Slack's link delimiters must not be rendered as a link, or it
	// would corrupt the <url|text> markup; the title still appears as plain text.
	bad := rssItem("Feed", "Tricky", "https://e/path?a=1|b=2", 1)
	if err := NewSlack(srv.URL, 10).Notify(context.Background(), []model.Item{bad}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}

	text := allText(payload)
	if !strings.Contains(text, "Tricky") {
		t.Errorf("title missing from output:\n%s", text)
	}
	if strings.Contains(text, "<https://e/path?a=1|b=2|") {
		t.Errorf("URL with a delimiter must not be rendered as a link:\n%s", text)
	}
}

func TestSlackNotify_emptySourceName_usesPlaceholder(t *testing.T) {
	srv, payload, _, _ := captureServer(t, http.StatusOK)

	item := rssItem("", "Orphan", "https://e/1", 1)
	if err := NewSlack(srv.URL, 10).Notify(context.Background(), []model.Item{item}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}

	if text := allText(payload); !strings.Contains(text, "unknown source") {
		t.Errorf("expected an '(unknown source)' header for the empty SourceName; got\n%s", text)
	}
}

func TestSlackNotify_emptyItemsIsNoop(t *testing.T) {
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		posted = true
	}))
	t.Cleanup(srv.Close)

	if err := NewSlack(srv.URL, 10).Notify(context.Background(), nil); err != nil {
		t.Fatalf("Notify(nil) error = %v", err)
	}
	if posted {
		t.Error("Notify(nil) must not POST anything")
	}
}

func TestSlackNotify_non2xxIsError(t *testing.T) {
	srv, _, _, _ := captureServer(t, http.StatusInternalServerError)

	err := NewSlack(srv.URL, 10).Notify(context.Background(),
		[]model.Item{rssItem("Feed", "t", "https://e/1", 1)})
	if err == nil {
		t.Fatal("Notify() to a 500 endpoint must return an error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %v, want it to mention the status code", err)
	}
}

func TestSlackNotify_transportErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // closed before the call: the POST fails at the transport layer

	err := NewSlack(url, 10).Notify(context.Background(),
		[]model.Item{rssItem("Feed", "t", "https://e/1", 1)})
	if err == nil {
		t.Fatal("Notify() to a closed server must return a transport error")
	}
}

func TestSlackNotify_cancelledContextPropagates(t *testing.T) {
	srv, _, _, _ := captureServer(t, http.StatusOK)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the request is built

	err := NewSlack(srv.URL, 10).Notify(ctx,
		[]model.Item{rssItem("Feed", "t", "https://e/1", 1)})
	if err == nil {
		t.Fatal("Notify() with a cancelled context must return an error")
	}
}
