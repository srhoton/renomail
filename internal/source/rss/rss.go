package rss

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"
	"golang.org/x/net/html"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/source"
)

// DefaultTimeout bounds a single feed fetch so an unresponsive server cannot
// stall the sync loop indefinitely. It is the shared default used by New, the
// provider registry, and the dump command.
const DefaultTimeout = 30 * time.Second

// snippetRunes is the maximum length, in runes, of the list-row preview.
const snippetRunes = 200

// Provider implements source.Provider; this assertion guards against signature
// drift breaking the contract silently.
var _ source.Provider = (*Provider)(nil)

// Provider is a single RSS/Atom feed exposed through the source.Provider
// interface. It carries the conditional-GET validators (ETag/Last-Modified)
// hydrated from the store; Fetch refreshes them, and SourceState exposes the
// updated values for the caller to persist.
//
// A Provider is safe for concurrent use: mu guards the mutable src validators
// so the sync engine (step 07) may Fetch feeds in parallel.
type Provider struct {
	mu     sync.Mutex
	src    model.Source
	url    string
	client *http.Client
}

// New builds a Provider for the feed at url, seeded with src (ID, Name, Kind and
// any stored validators). A nil client is replaced with one carrying a sane
// timeout.
func New(src model.Source, url string, client *http.Client) *Provider {
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	return &Provider{src: src, url: url, client: client}
}

// ID returns the stable feed source identifier.
func (p *Provider) ID() string { return p.src.ID }

// Name returns the human-readable feed name.
func (p *Provider) Name() string { return p.src.Name }

// Kind reports that this provider yields RSS items.
func (p *Provider) Kind() model.Kind { return model.KindRSS }

// SourceState returns the provider's Source with any validators refreshed by the
// most recent Fetch, so the caller can persist them for the next conditional GET.
func (p *Provider) SourceState() model.Source {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.src
}

// Fetch retrieves the feed and maps every entry onto a model.Item. It issues a
// conditional GET using the stored ETag/Last-Modified; a 304 Not Modified yields
// (nil, nil). The since argument is accepted to satisfy source.Provider but is
// not used: a feed returns its full current window and the store's idempotent
// upsert (keyed on the stable Item ID) deduplicates re-seen entries.
func (p *Provider) Fetch(ctx context.Context, _ time.Time) ([]model.Item, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request %s: %w", p.url, err)
	}
	// Snapshot the validators under the lock (released before the network I/O)
	// so a concurrent Fetch on the same Provider cannot race the src fields.
	p.mu.Lock()
	etag, lastModified := p.src.ETag, p.src.LastModified
	p.mu.Unlock()
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", p.url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		return nil, nil // polite no-op: nothing changed since last fetch
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %d", p.url, resp.StatusCode)
	}

	// Capture validators for the next conditional GET.
	p.mu.Lock()
	p.src.ETag = resp.Header.Get("ETag")
	p.src.LastModified = resp.Header.Get("Last-Modified")
	p.mu.Unlock()

	// A fresh parser per fetch keeps Provider safe to use concurrently (the
	// sync engine in step 07 fetches feeds in parallel); gofeed.NewParser is
	// cheap and gofeed.Parser is not documented as safe for concurrent use.
	feed, err := gofeed.NewParser().Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", p.url, err)
	}

	now := time.Now()
	items := make([]model.Item, 0, len(feed.Items))
	for _, e := range feed.Items {
		items = append(items, p.toItem(feed, e, now))
	}
	return items, nil
}

// Body is a no-op for RSS: the feed delivers entry content with the list, so the
// body is already populated by Fetch.
func (p *Provider) Body(context.Context, *model.Item) error { return nil }

// toItem maps a gofeed entry onto the unified model.Item. The native id is the
// entry GUID (falling back to its link); the stable Item ID is derived from the
// source ID and that native id so re-fetches upsert in place.
func (p *Provider) toItem(feed *gofeed.Feed, e *gofeed.Item, now time.Time) model.Item {
	body := firstNonEmpty(e.Content, e.Description)
	text := plainText(body) // strip once; reused for both Snippet and BodyText

	published := now
	switch {
	case e.PublishedParsed != nil:
		published = *e.PublishedParsed
	case e.UpdatedParsed != nil:
		published = *e.UpdatedParsed
	}

	// The native id is the entry GUID (or its link). When a feed supplies
	// neither, fall back to a content hash so distinct entries do not collapse
	// onto a single stored ID. Caveat: for such id-less entries the derived
	// Item.ID shifts if the title or body text changes, so an edit re-adds the
	// item as unread — unavoidable without a stable provider id, and strictly
	// better than every id-less entry sharing one row.
	native := firstNonEmpty(e.GUID, e.Link)
	if native == "" {
		native = model.StableID(e.Title, text)
	}

	author := ""
	if len(e.Authors) > 0 && e.Authors[0] != nil {
		author = e.Authors[0].Name
	}

	return model.Item{
		ID:         model.StableID(p.src.ID, native),
		Kind:       model.KindRSS,
		SourceID:   p.src.ID,
		SourceName: firstNonEmpty(p.src.Name, feed.Title),
		Author:     author,
		Title:      e.Title,
		Snippet:    truncateRunes(text, snippetRunes),
		URL:        e.Link,
		NativeID:   native,
		Published:  published,
		Fetched:    now,
		BodyHTML:   body,
		BodyText:   text,
	}
}

// plainText strips HTML markup from s, unescapes entities, and collapses runs of
// whitespace into single spaces. Malformed markup degrades gracefully: whatever
// text the tokenizer recovers is returned.
func plainText(s string) string {
	if !strings.ContainsAny(s, "<&") {
		return strings.Join(strings.Fields(s), " ") // no markup: collapse only
	}
	var b strings.Builder
	b.Grow(len(s)) // output is bounded by the input length
	z := html.NewTokenizer(strings.NewReader(s))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.Join(strings.Fields(b.String()), " ")
		case html.TextToken:
			b.Write(z.Text()) // the tokenizer unescapes entities for us
		default:
			// A space at element boundaries keeps adjacent block text from
			// running together (e.g. "<p>a</p><p>b</p>" -> "a b").
			b.WriteByte(' ')
		}
	}
}

// truncateRunes returns s limited to at most n runes, appending an ellipsis when
// it trims. It scans byte offsets to find the cut point, avoiding a full []rune
// allocation over the whole string.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return strings.TrimRight(s[:i], " ") + "…"
		}
		count++
	}
	return s // s has n or fewer runes
}
