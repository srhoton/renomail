package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/srhoton/renomail/internal/model"
)

// slackDefaultMaxItems caps how many item lines a single digest lists before
// collapsing the remainder into a "…and N more" line, keeping the message readable.
const slackDefaultMaxItems = 10

// slackTimeout bounds a single webhook POST. The engine also wraps the call in its
// own timeout; this is the transport-level backstop.
const slackTimeout = 10 * time.Second

// Slack posts "new items arrived" digests to a Slack incoming webhook. One Slack
// value is reused across sweeps. It is dependency-free (net/http + encoding/json):
// the engine calls Notify once per steady-state sweep with that sweep's coalesced
// new items, and Notify groups them by source and renders a Block Kit message.
type Slack struct {
	webhookURL string
	client     *http.Client
	max        int
}

// NewSlack builds a Slack notifier posting to webhookURL. max bounds the number of
// listed items (<= 0 selects the default); the HTTP client carries a bounded
// timeout so a hung webhook cannot stall the caller.
func NewSlack(webhookURL string, max int) *Slack {
	if max <= 0 {
		max = slackDefaultMaxItems
	}
	return &Slack{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: slackTimeout},
		max:        max,
	}
}

// slackPayload is the subset of the Slack incoming-webhook message shape we emit:
// a plain-text fallback (used for notifications/accessibility) plus mrkdwn section
// blocks for the rich rendering.
type slackPayload struct {
	Text   string       `json:"text"`
	Blocks []slackBlock `json:"blocks,omitempty"`
}

// slackBlock is one Block Kit block in the message; we only emit "section" blocks.
type slackBlock struct {
	Type string     `json:"type"`
	Text *slackText `json:"text,omitempty"`
}

// slackText is a Block Kit text object; we always use the "mrkdwn" type.
type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Notify posts a single digest of items to the configured webhook. An empty item
// list is a no-op. A transport error or any non-2xx response is returned for the
// caller (the engine) to surface; the message itself is built from the items'
// titles, sources, senders, and links.
func (s *Slack) Notify(ctx context.Context, items []model.Item) error {
	if len(items) == 0 {
		return nil
	}

	body, err := json.Marshal(s.build(items))
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("post slack webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook: status %d", resp.StatusCode)
	}
	return nil
}

// build renders items into a Slack message: a summary header, one mrkdwn section per
// source (sources sorted by name, items newest first), and — when the listed items
// exceed max — a trailing "…and N more" line. Output ordering is fully deterministic
// so the payload is stable and testable.
func (s *Slack) build(items []model.Item) slackPayload {
	groups := groupBySource(items)

	sourceWord := "sources"
	if len(groups) == 1 {
		sourceWord = "source"
	}
	summary := fmt.Sprintf("📬 *renomail* — %d new across %d %s", len(items), len(groups), sourceWord)

	blocks := []slackBlock{section(summary)}

	listed, truncated := 0, 0
	for _, g := range groups {
		if listed >= s.max {
			truncated += len(g.items)
			continue
		}
		name := g.name
		if strings.TrimSpace(name) == "" {
			name = "(unknown source)"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "*%s*", escapeMrkdwn(name))
		for _, it := range g.items {
			if listed >= s.max {
				truncated++
				continue
			}
			b.WriteString("\n• ")
			b.WriteString(itemLine(it))
			listed++
		}
		blocks = append(blocks, section(b.String()))
	}
	if truncated > 0 {
		blocks = append(blocks, section(fmt.Sprintf("_…and %d more_", truncated)))
	}

	return slackPayload{
		Text:   fmt.Sprintf("renomail: %d new across %d %s", len(items), len(groups), sourceWord),
		Blocks: blocks,
	}
}

// sourceGroup is one source's items, ready to render.
type sourceGroup struct {
	name  string
	items []model.Item
}

// groupBySource buckets items by SourceName, sorts the buckets by name, and orders
// each bucket newest-first (Title breaks ties) for deterministic output.
func groupBySource(items []model.Item) []sourceGroup {
	byName := make(map[string][]model.Item)
	for _, it := range items {
		byName[it.SourceName] = append(byName[it.SourceName], it)
	}
	groups := make([]sourceGroup, 0, len(byName))
	for name, its := range byName {
		sort.Slice(its, func(i, j int) bool {
			if !its[i].Published.Equal(its[j].Published) {
				return its[i].Published.After(its[j].Published)
			}
			return its[i].Title < its[j].Title
		})
		groups = append(groups, sourceGroup{name: name, items: its})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].name < groups[j].name })
	return groups
}

// itemLine renders one item as a Slack mrkdwn bullet: a linked title when a URL is
// present, plus the sender for email items.
func itemLine(it model.Item) string {
	title := escapeMrkdwn(strings.TrimSpace(it.Title))
	if title == "" {
		title = "(untitled)"
	}
	line := title
	// Only render the <url|text> link form when the URL is free of the characters
	// Slack uses to delimit links; otherwise a stray | or > would corrupt the markup,
	// so fall back to the plain (still escaped) title.
	if u := strings.TrimSpace(it.URL); u != "" && !strings.ContainsAny(u, "<>|") {
		line = fmt.Sprintf("<%s|%s>", u, title)
	}
	if it.Kind == model.KindEmail {
		if author := strings.TrimSpace(it.Author); author != "" {
			line += " — from " + escapeMrkdwn(author)
		}
	}
	return line
}

// section wraps mrkdwn text in a Slack section block.
func section(mrkdwn string) slackBlock {
	return slackBlock{Type: "section", Text: &slackText{Type: "mrkdwn", Text: mrkdwn}}
}

// escapeMrkdwn escapes the three characters Slack treats specially in message text,
// so a title containing them renders literally rather than corrupting the markup.
func escapeMrkdwn(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
