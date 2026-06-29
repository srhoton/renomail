// Package gmail implements the Gmail source.Provider: a per-account, read-only
// Gmail API client with an OAuth2 loopback consent flow (auth.go), cheap
// metadata-only listing for the feed (Fetch), and lazy MIME body loading (Body,
// mime.go). One Provider maps to one Gmail account; the account email doubles as
// the human-readable name and the basis of the stable source ID.
package gmail

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/mail"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/srhoton/renomail/internal/config"
	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/source"
)

// metadataHeaders are the only headers Fetch requests for each message: enough to
// build a list row without downloading bodies. Body() later fetches the full
// payload on demand.
var metadataHeaders = []string{"From", "Subject", "Date"}

// Provider implements source.Provider for a single Gmail account. The compile
// assertions guard against signature drift breaking either contract silently; the
// second declares that this provider can also write read state back (SetRead).
var (
	_ source.Provider   = (*Provider)(nil)
	_ source.ReadSyncer = (*Provider)(nil)
)

// ErrReauthorize signals that the stored token predates the gmail.modify scope, so a
// write was refused (HTTP 403). It is sentinel so the UI can show an actionable
// "re-run `renomail auth <account>`" hint instead of a raw API error; reads continue
// to work on the old read-only grant.
var ErrReauthorize = errors.New("gmail: token lacks write scope; run `renomail auth <account>` to re-authorize")

// modifyBatchSize bounds one BatchModify call to the Gmail API's documented limit of
// 1000 message ids; larger sets are chunked across calls.
const modifyBatchSize = 1000

// Provider is one configured Gmail account exposed through source.Provider. It is
// safe for concurrent Fetch/Body calls: svc is an immutable client and the
// remaining fields are read-only after construction (the underlying
// *gmail.Service is itself safe for concurrent use).
type Provider struct {
	account  string
	id       string // "gmail:" + account, precomputed once (invariant per provider)
	svc      *gmail.Service
	lookback time.Duration
}

// New constructs a Provider for account using the Desktop-app credentials at
// paths.Credentials and the OAuth token at paths.TokenFile(account). A missing
// token yields ErrNotAuthorized so the caller can prompt for `renomail auth`. The
// oauth2 client transparently refreshes the access token from the stored refresh
// token, so normal runs never need the browser again.
func New(ctx context.Context, paths config.Paths, account string, lookback time.Duration) (*Provider, error) {
	cfg, err := oauthConfig(paths.Credentials)
	if err != nil {
		return nil, err
	}
	tok, err := loadToken(paths.TokenFile(account))
	if err != nil {
		return nil, err // ErrNotAuthorized (missing) or a wrapped parse error
	}
	svc, err := gmail.NewService(ctx, option.WithHTTPClient(cfg.Client(ctx, tok)))
	if err != nil {
		return nil, fmt.Errorf("gmail service for %s: %w", account, err)
	}
	return newWithService(account, svc, lookback), nil
}

// newWithService builds a Provider around an already-constructed service. It backs
// the offline tests (which point a *gmail.Service at an httptest server) and is
// unexported because production code always goes through New. The source id is
// computed once here so the per-message mapping does not rebuild it.
func newWithService(account string, svc *gmail.Service, lookback time.Duration) *Provider {
	return &Provider{account: account, id: "gmail:" + account, svc: svc, lookback: lookback}
}

// ID returns the stable source identifier ("gmail:<account>"), distinct from any
// RSS feed id and stable across runs so upserts and per-source state line up.
func (p *Provider) ID() string { return p.id }

// Name returns the account email, used as the display name in the feed.
func (p *Provider) Name() string { return p.account }

// Kind reports that this provider yields email items.
func (p *Provider) Kind() model.Kind { return model.KindEmail }

// fetchConcurrency bounds the per-message metadata Get calls issued within one
// Fetch. A cold start over a large inbox is dominated by these round-trips, so
// fetching them concurrently (rather than serially) is the difference between a
// snappy first sweep and a multi-second stall. Note this bounds one account's
// Gets; the sync engine fetches several providers concurrently too, so the
// process-wide ceiling of in-flight Gmail Gets is (engine fan-out) ×
// fetchConcurrency. With the handful of accounts a personal reader configures this
// stays comfortably within Gmail's per-user limits; it is the knob to lower if a
// large account count ever pressures the shared project quota.
const fetchConcurrency = 8

// Fetch lists inbox messages and returns one body-less model.Item per message
// (headers + snippet only). On a cold start (zero since) it scans the configured
// lookback window; once a LastSync is known it asks only for messages after it.
// Listing collects the message ids first, then the per-message metadata is fetched
// concurrently (bounded) into a position-indexed slice, so the result preserves the
// list order while cold-start latency stays low.
//
// Per-message Gets are resilient: a single failed Get does not abort the others or
// discard the messages already fetched. Fetch returns the items it successfully
// retrieved (compacted, still in list order) together with the first error, so the
// caller can persist the partial result and retry the rest on the next sweep rather
// than re-fetching the whole window. Bodies are loaded lazily by Body.
func (p *Provider) Fetch(ctx context.Context, since time.Time) ([]model.Item, error) {
	now := time.Now()
	var ids []string
	call := p.svc.Users.Messages.List("me").Q(p.query(since))
	err := call.Pages(ctx, func(page *gmail.ListMessagesResponse) error {
		// Grow once per page (the page size is known) so the per-message appends
		// do not trigger repeated reallocations across a large inbox.
		ids = slices.Grow(ids, len(page.Messages))
		for _, ref := range page.Messages {
			ids = append(ids, ref.Id)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list messages for %s: %w", p.account, err)
	}

	// Fan the metadata Gets out with a bounded worker pool, writing each result to
	// its list position so distinct indices never race and order is preserved. A
	// plain errgroup (no derived context) is used deliberately so one Get's failure
	// does not cancel the in-flight others — every id is attempted, and Wait reports
	// the first error while the successes remain in the slice.
	items := make([]model.Item, len(ids))
	var g errgroup.Group
	g.SetLimit(fetchConcurrency)
	for i, id := range ids {
		g.Go(func() error {
			msg, err := p.svc.Users.Messages.Get("me", id).
				Format("metadata").MetadataHeaders(metadataHeaders...).
				Context(ctx).Do()
			if err != nil {
				return fmt.Errorf("get message %s: %w", id, err)
			}
			items[i] = p.toItem(msg, now)
			return nil
		})
	}
	waitErr := g.Wait()

	// Compact out the gaps left by any failed Gets, preserving list order.
	fetched := items[:0]
	for _, it := range items {
		if it.NativeID != "" {
			fetched = append(fetched, it)
		}
	}
	if waitErr != nil {
		return fetched, fmt.Errorf("fetch messages for %s: %w", p.account, waitErr)
	}
	return fetched, nil
}

// query builds the Gmail search expression for a fetch. With no prior sync it
// bounds the scan to the lookback window (newer_than:Nd); afterwards it requests
// only messages received after the last sync (after:<unix>), so steady-state syncs
// stay cheap. Gmail's after: is inclusive at second granularity, so the message(s)
// at exactly the previous LastSync are re-listed each sweep; this is harmless
// because the store upsert is idempotent and preserves local read state.
func (p *Provider) query(since time.Time) string {
	if since.IsZero() {
		return fmt.Sprintf("in:inbox newer_than:%dd", days(p.lookback))
	}
	return fmt.Sprintf("in:inbox after:%d", since.Unix())
}

// Body lazily loads an item's full content: it fetches the message with the full
// MIME payload and decodes the HTML and text bodies into the item in place. The
// Gmail message id is recovered from item.NativeID (set by toItem and persisted
// by the store), so Body never has to parse the web URL.
func (p *Provider) Body(ctx context.Context, item *model.Item) error {
	if item.NativeID == "" {
		return fmt.Errorf("gmail body: item %s has no native id", item.ID)
	}
	msg, err := p.svc.Users.Messages.Get("me", item.NativeID).Format("full").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("get full message %s: %w", item.NativeID, err)
	}
	item.BodyHTML, item.BodyText = selectBodies(msg.Payload)
	return nil
}

// SetRead reflects the read flag for the given Gmail message ids at the server by
// toggling the UNREAD label: removing it marks read, adding it marks unread. It is the
// source.ReadSyncer write-back path the UI invokes when renomail marks mail read/unread
// locally. nativeIDs are the Gmail message ids stored as Item.NativeID (exactly what
// the modify API takes); empty ids are skipped and an empty set is a no-op. Calls are
// chunked to the API's 1000-id batchModify limit. A 403 from a token still on the old
// read-only scope is mapped to ErrReauthorize so the caller can prompt a one-time
// re-auth rather than surface a raw API error.
func (p *Provider) SetRead(ctx context.Context, nativeIDs []string, read bool) error {
	ids := nonEmpty(nativeIDs)
	if len(ids) == 0 {
		return nil
	}
	req := &gmail.BatchModifyMessagesRequest{}
	if read {
		req.RemoveLabelIds = []string{"UNREAD"}
	} else {
		req.AddLabelIds = []string{"UNREAD"}
	}
	for chunk := range slices.Chunk(ids, modifyBatchSize) {
		req.Ids = chunk
		if err := p.svc.Users.Messages.BatchModify("me", req).Context(ctx).Do(); err != nil {
			if isInsufficientScope(err) {
				return fmt.Errorf("%w (%s)", ErrReauthorize, p.account)
			}
			return fmt.Errorf("gmail set read for %s: %w", p.account, err)
		}
	}
	return nil
}

// nonEmpty returns ids with blank entries removed, so a stray empty native id never
// reaches the modify API (which would reject the whole batch).
func nonEmpty(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			out = append(out, id)
		}
	}
	return out
}

// isInsufficientScope reports whether err is the Gmail API's 403 for a token that was
// granted only gmail.readonly and so cannot modify labels — the signal that the account
// must re-authorize for the upgraded gmail.modify scope. It matches a 403 carrying an
// "insufficient ..." reason/message rather than every 403, so a genuine permission
// error is not mislabeled as a re-auth prompt.
func isInsufficientScope(err error) bool {
	var gerr *googleapi.Error
	if !errors.As(err, &gerr) || gerr.Code != http.StatusForbidden {
		return false
	}
	if strings.Contains(strings.ToLower(gerr.Message), "insufficient") {
		return true
	}
	for _, e := range gerr.Errors {
		if e.Reason == "insufficientPermissions" || strings.Contains(strings.ToLower(e.Message), "insufficient") {
			return true
		}
	}
	return false
}

// toItem maps a Gmail message (metadata format) onto the unified model.Item. The
// native id is the Gmail message id; the stable Item.ID is derived from the source
// id and that native id so re-fetches upsert in place. The body is left empty and
// loaded later by Body.
func (p *Provider) toItem(msg *gmail.Message, fetched time.Time) model.Item {
	var h map[string]string
	if msg.Payload != nil {
		h = headers(msg.Payload.Headers)
	}
	return model.Item{
		ID:         model.StableID(p.ID(), msg.Id),
		Kind:       model.KindEmail,
		SourceID:   p.ID(),
		SourceName: p.account,
		Author:     h["from"],
		Title:      h["subject"],
		Snippet:    html.UnescapeString(msg.Snippet),
		URL:        fmt.Sprintf("https://mail.google.com/mail/u/?authuser=%s#all/%s", p.account, msg.Id),
		NativeID:   msg.Id,
		Published:  parseDate(h["date"], msg.InternalDate),
		Fetched:    fetched,
	}
}

// headers collapses a message part's header list into a lowercase-keyed map for
// case-insensitive lookup (RFC 5322 header names are case-insensitive). When a
// header repeats, the first occurrence wins.
func headers(hs []*gmail.MessagePartHeader) map[string]string {
	m := make(map[string]string, len(hs))
	for _, hdr := range hs {
		if hdr == nil {
			continue
		}
		key := strings.ToLower(hdr.Name)
		if _, seen := m[key]; !seen {
			m[key] = hdr.Value
		}
	}
	return m
}

// parseDate resolves a message's timestamp, preferring the RFC 5322 Date header
// and falling back to Gmail's InternalDate (epoch milliseconds) when the header is
// absent or unparseable. The result is always in UTC for stable sorting.
func parseDate(dateHeader string, internalMillis int64) time.Time {
	if dateHeader != "" {
		if t, err := mail.ParseDate(dateHeader); err == nil {
			return t.UTC()
		}
	}
	if internalMillis > 0 {
		return time.UnixMilli(internalMillis).UTC()
	}
	return time.Time{}
}

// days converts a lookback duration to whole days for the Gmail "newer_than:Nd"
// query, rounding up so a sub-day window still scans at least one day and a
// non-day-aligned window does not silently drop its tail.
func days(d time.Duration) int {
	if d <= 0 {
		return 1
	}
	n := int((d + 24*time.Hour - 1) / (24 * time.Hour))
	if n < 1 {
		return 1
	}
	return n
}
