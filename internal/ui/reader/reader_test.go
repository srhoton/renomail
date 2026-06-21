package reader

import (
	"strings"
	"testing"
	"time"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/ui/styles"
)

func TestReader_headerContentView(t *testing.T) {
	m := New(styles.DefaultStyles())
	m.SetSize(80, 24)

	it := model.Item{
		Title:      "An Article",
		Author:     "Jane Doe",
		SourceName: "Hacker News",
		Published:  time.Date(2026, 6, 21, 9, 30, 0, 0, time.UTC),
	}
	m.SetHeader(it)
	m.SetContent("the rendered body content")

	v := m.View()
	for _, want := range []string{"An Article", "Jane Doe", "Hacker News", "2026-06-21", "the rendered body content"} {
		if !strings.Contains(v, want) {
			t.Errorf("reader view missing %q:\n%s", want, v)
		}
	}
}

func TestReader_headerOmitsMissingAuthorAndDate(t *testing.T) {
	m := New(styles.DefaultStyles())
	m.SetSize(80, 24)

	// No author, zero date: the header is just the title and source.
	m.SetHeader(model.Item{Title: "Bare", SourceName: "Feed"})
	v := m.View()
	if !strings.Contains(v, "Bare") || !strings.Contains(v, "Feed") {
		t.Errorf("header missing title/source:\n%s", v)
	}
}

func TestReader_setSizeTinyHeight(t *testing.T) {
	m := New(styles.DefaultStyles())
	// Height smaller than the header must not produce a negative viewport height.
	m.SetSize(40, 1)
	m.SetContent("body")
	_ = m.View() // must not panic
}
