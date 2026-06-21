// Package rss implements the RSS/Atom Provider and OPML import: it turns OPML
// files (and one-off feeds in config) into Sources, fetches each feed with a
// conditional GET, and maps entries onto the unified model.Item.
package rss

import (
	"fmt"

	"github.com/gilliek/go-opml/opml"

	"github.com/srhoton/renomail/internal/model"
)

// FeedRef pairs a feed's Source with the URL it was loaded from. The Source ID
// is a one-way hash of the URL (see feedID), so the registry keeps the URL
// alongside the Source to construct a Provider.
type FeedRef struct {
	Source model.Source
	URL    string
}

// ImportOPML reads an OPML file and returns one FeedRef per feed outline.
// OPML nests outlines (categories contain feeds), so it walks recursively;
// outlines without an xmlUrl (pure category nodes) are skipped.
func ImportOPML(path string) ([]FeedRef, error) {
	doc, err := opml.NewOPMLFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("read opml %s: %w", path, err)
	}

	var out []FeedRef
	var walk func(outlines []opml.Outline)
	walk = func(outlines []opml.Outline) {
		for _, o := range outlines {
			if o.XMLURL != "" {
				out = append(out, FeedRef{
					Source: model.Source{
						ID:   feedID(o.XMLURL),
						Name: firstNonEmpty(o.Title, o.Text, o.XMLURL),
						Kind: model.KindRSS,
					},
					URL: o.XMLURL,
				})
			}
			walk(o.Outlines)
		}
	}
	walk(doc.Body.Outlines)
	return out, nil
}

// NewFeedRef builds a FeedRef for a single feed configured without OPML (a
// one-off [[feed]] entry). The title falls back to the URL when empty.
func NewFeedRef(url, title string) FeedRef {
	return FeedRef{
		Source: model.Source{
			ID:   feedID(url),
			Name: firstNonEmpty(title, url),
			Kind: model.KindRSS,
		},
		URL: url,
	}
}

// feedID derives a stable, namespaced source ID from a feed URL. The "rss:"
// prefix distinguishes feed sources from Gmail account sources at a glance.
func feedID(url string) string { return "rss:" + model.StableID("rss", url) }

// firstNonEmpty returns the first argument that is not the empty string, or ""
// if all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
