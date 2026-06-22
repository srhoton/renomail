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

func TestModel_setItems_returnsRunnableCmd(t *testing.T) {
	m := newSized()
	// The returned command is propagated to the Bubble Tea runtime by the caller
	// (app.go returns it from the itemsLoadedMsg case); exercise it here so the
	// contract is asserted rather than silently discarded.
	if cmd := m.SetItems(testItems()); cmd != nil {
		if msg := cmd(); msg == nil {
			t.Error("SetItems cmd produced a nil message")
		}
	}
	if _, ok := m.Selected(); !ok {
		t.Error("Selected() ok = false after SetItems")
	}
}

func TestModel_setReadLocal_flipsAddressedRow(t *testing.T) {
	m := newSized()
	m.SetItems(testItems())

	// "b" is the read item; mark it unread by id while "a" stays selected.
	m.SetReadLocal("b", false)

	for _, li := range m.list.Items() {
		r := li.(row)
		if r.item.ID == "b" && r.item.Read {
			t.Error("item b still read after SetReadLocal(b,false)")
		}
		if r.item.ID == "a" && r.item.Read {
			t.Error("item a wrongly changed by SetReadLocal(b,...)")
		}
	}
}

func TestModel_setReadLocal_unknownIDIsNoOp(t *testing.T) {
	m := newSized()
	m.SetItems(testItems())

	m.SetReadLocal("does-not-exist", true) // must not panic or alter rows

	for _, li := range m.list.Items() {
		r := li.(row)
		if r.item.ID == "a" && r.item.Read {
			t.Error("item a changed by SetReadLocal on unknown id")
		}
		if r.item.ID == "b" && !r.item.Read {
			t.Error("item b changed by SetReadLocal on unknown id")
		}
	}
}

func TestModel_removeLocal_dropsAddressedRow(t *testing.T) {
	m := newSized()
	m.SetItems(testItems())

	m.RemoveLocal("a") // drop the unread item

	items := m.list.Items()
	if len(items) != 1 {
		t.Fatalf("len(items) = %d after RemoveLocal, want 1", len(items))
	}
	if got := items[0].(row).item.ID; got != "b" {
		t.Errorf("remaining item id = %q, want %q", got, "b")
	}
}

func TestModel_removeLocal_unknownIDIsNoOp(t *testing.T) {
	m := newSized()
	m.SetItems(testItems())

	m.RemoveLocal("does-not-exist") // must not panic or drop a row

	if got := len(m.list.Items()); got != 2 {
		t.Errorf("len(items) = %d after RemoveLocal on unknown id, want 2", got)
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

func TestView_selectedLineCarriesCursorOthersDoNot(t *testing.T) {
	m := newSized()
	m.SetItems(testItems()) // index 0 ("Unread One") is selected by default

	v := m.View()
	lines := strings.Split(v, "\n")

	findLine := func(title string) string {
		t.Helper()
		for _, ln := range lines {
			if strings.Contains(ln, title) {
				return ln
			}
		}
		t.Fatalf("view has no line containing %q:\n%s", title, v)
		return ""
	}

	// The selected row shows the "> " cursor; the other shows the blank "  " gutter.
	// (Plain glyphs, so this holds regardless of lipgloss's color profile in tests.)
	if sel := findLine("Unread One"); !strings.Contains(sel, "> ") {
		t.Errorf("selected line missing the %q cursor:\n%s", "> ", sel)
	}
	if other := findLine("Read Two"); strings.Contains(other, "> ") {
		t.Errorf("non-selected line wrongly shows the %q cursor:\n%s", "> ", other)
	}
}

func TestView_narrowWidth_selectedRowStaysSingleLine(t *testing.T) {
	m := New(styles.DefaultStyles())
	m.SetSize(30, 6)            // narrow enough that the title budget clamps to minTitleCols
	m.SetItems(testItems()[:1]) // a single, selected row

	v := m.View()
	nonBlank := 0
	for ln := range strings.SplitSeq(v, "\n") {
		if strings.TrimSpace(stripANSI(ln)) != "" {
			nonBlank++
		}
	}
	if nonBlank != 1 {
		t.Errorf("selected row at narrow width spans %d visual lines, want 1:\n%q", nonBlank, v)
	}
}

// stripANSI removes SGR escape sequences so a rendered line can be checked for
// blankness regardless of lipgloss's color/background output.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			// Skip up to and including the terminating 'm' of the CSI sequence.
			for i < len(s) && s[i] != 'm' {
				i++
			}
			if i < len(s) {
				i++ // consume the 'm'
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
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
