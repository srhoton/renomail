package feed

import (
	"strings"
	"testing"
	"time"

	runewidth "github.com/mattn/go-runewidth"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/ui/styles"
)

func testItems() []model.Item {
	now := time.Now()
	return []model.Item{
		{ID: "a", Kind: model.KindRSS, SourceName: "Feed A", Title: "Unread One", Published: now.Add(-time.Hour), Read: false},
		{ID: "b", Kind: model.KindRSS, SourceName: "Feed B", Title: "Read Two", Published: now.Add(-2 * time.Hour), Read: true},
	}
}

func newSized() Model {
	m := New(styles.DefaultStyles())
	m.SetSize(100, 20)
	return m
}

func TestModel_setItemsAndSelected(t *testing.T) {
	m := newSized()
	m.SetItems(testItems())

	it, ok := m.Selected()
	if !ok {
		t.Fatal("Selected() ok = false after SetItems")
	}
	if it.Title != "Unread One" {
		t.Errorf("selected title = %q, want %q", it.Title, "Unread One")
	}
}

func TestModel_selectedEmpty(t *testing.T) {
	m := newSized()
	if _, ok := m.Selected(); ok {
		t.Error("Selected() ok = true on empty feed, want false")
	}
}

func TestModel_markSelectedRead(t *testing.T) {
	m := newSized()
	m.SetItems(testItems())

	m.MarkSelectedRead()
	it, ok := m.Selected()
	if !ok {
		t.Fatal("Selected() ok = false")
	}
	if !it.Read {
		t.Error("selected item not marked read after MarkSelectedRead")
	}
}

func TestView_rendersRowsWithDots(t *testing.T) {
	m := newSized()
	m.SetItems(testItems())

	v := m.View()
	for _, want := range []string{"Unread One", "Read Two", "●", "○", "Feed A"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"fits", "hello", 10, "hello"},
		{"exact", "abcde", 5, "abcde"},
		{"truncates", "hello world", 5, "hell…"},
		{"rune-safe", "héllo", 4, "hél…"},
		{"trims trailing space", "ab cdef", 3, "ab…"},
		{"width one", "ab", 1, "…"},
		{"zero width", "anything", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncate(tt.in, tt.n); got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}

func TestTruncate_wideRunesStayWithinDisplayWidth(t *testing.T) {
	// CJK runes are two display cells each; the result (incl. ellipsis) must not
	// exceed the requested column budget.
	const in = "日本語のタイトル"
	for _, n := range []int{1, 4, 5, 8} {
		got := truncate(in, n)
		if w := runewidth.StringWidth(got); w > n {
			t.Errorf("truncate(%q, %d) = %q has display width %d, want <= %d", in, n, got, w, n)
		}
	}
}
