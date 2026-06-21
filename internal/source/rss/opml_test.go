package rss

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/srhoton/renomail/internal/model"
)

func TestImportOPML_nestedOutlines(t *testing.T) {
	refs, err := ImportOPML(filepath.Join("testdata", "sample.opml"))
	if err != nil {
		t.Fatalf("ImportOPML() error = %v", err)
	}

	// Both the top-level feed and the feed nested under a category must appear;
	// the category outline itself (no xmlUrl) must be skipped.
	want := []FeedRef{
		{Source: model.Source{ID: feedID("https://example.com/top.xml"), Name: "Top Feed", Kind: model.KindRSS}, URL: "https://example.com/top.xml"},
		{Source: model.Source{ID: feedID("https://example.com/nested.xml"), Name: "Nested Feed", Kind: model.KindRSS}, URL: "https://example.com/nested.xml"},
	}
	if len(refs) != len(want) {
		t.Fatalf("ImportOPML() returned %d refs, want %d: %+v", len(refs), len(want), refs)
	}
	for i, w := range want {
		if refs[i] != w {
			t.Errorf("ref[%d] = %+v, want %+v", i, refs[i], w)
		}
	}
}

func TestFeedID_stableAndNamespaced(t *testing.T) {
	const url = "https://example.com/top.xml"
	id1 := feedID(url)
	id2 := feedID(url)
	if id1 != id2 {
		t.Errorf("feedID not deterministic: %q != %q", id1, id2)
	}
	if !strings.HasPrefix(id1, "rss:") {
		t.Errorf("feedID = %q, want %q prefix", id1, "rss:")
	}
	if feedID(url) == feedID("https://example.com/other.xml") {
		t.Error("feedID collided for distinct URLs")
	}
}

func TestImportOPML_missingFile(t *testing.T) {
	if _, err := ImportOPML(filepath.Join("testdata", "does-not-exist.opml")); err == nil {
		t.Fatal("ImportOPML() error = nil, want error for missing file")
	}
}

func TestNewFeedRef_titleFallback(t *testing.T) {
	const url = "https://example.com/one-off.xml"
	ref := NewFeedRef(url, "")
	if ref.Source.Name != url {
		t.Errorf("Name = %q, want URL fallback %q", ref.Source.Name, url)
	}
	if ref.Source.ID != feedID(url) {
		t.Errorf("ID = %q, want %q", ref.Source.ID, feedID(url))
	}
	if ref.URL != url {
		t.Errorf("URL = %q, want %q", ref.URL, url)
	}
	if named := NewFeedRef(url, "Custom"); named.Source.Name != "Custom" {
		t.Errorf("Name = %q, want %q", named.Source.Name, "Custom")
	}
}
