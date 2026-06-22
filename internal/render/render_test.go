package render

import (
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/srhoton/renomail/internal/model"
)

// ansiRE strips SGR escape sequences so assertions look at the rendered text,
// not the exact styling bytes (which vary by glamour version/theme).
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func plain(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

func TestStyleForBackground(t *testing.T) {
	if got := styleForBackground(true); got == "" || got == styleForBackground(false) {
		t.Errorf("dark style %q must be non-empty and distinct from light", got)
	}
	if got := styleForBackground(false); got == "" {
		t.Error("light style must be non-empty")
	}
}

func TestRender_html_convertsAndStripsScripts(t *testing.T) {
	r, err := New(80)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	const html = `<h1>Title</h1>
<ul><li>one</li><li>two</li></ul>
<p>See <a href="https://example.com">link</a> and <code>snippet</code>.</p>
<script>alert('xss-payload')</script>`

	out, err := r.Render(model.Item{BodyHTML: html})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	got := plain(out)

	for _, want := range []string{"Title", "one", "two", "link", "snippet"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\n---\n%s", want, got)
		}
	}
	// The <script> body must never survive into terminal output.
	for _, banned := range []string{"alert", "xss-payload"} {
		if strings.Contains(got, banned) {
			t.Errorf("rendered output leaked script content %q\n---\n%s", banned, got)
		}
	}
}

func TestRender_emptyHTML_fallsBackToBodyText(t *testing.T) {
	r, err := New(80)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	out, err := r.Render(model.Item{BodyText: "plain body fallback text"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got := plain(out); !strings.Contains(got, "plain body fallback text") {
		t.Errorf("fallback text missing from output: %q", got)
	}
}

func TestRender_blankHTMLWhitespace_usesBodyText(t *testing.T) {
	r, err := New(80)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	// BodyHTML that is only whitespace must be treated as empty so the reader
	// shows the text fallback rather than an empty render.
	out, err := r.Render(model.Item{BodyHTML: "   \n\t ", BodyText: "used the text"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got := plain(out); !strings.Contains(got, "used the text") {
		t.Errorf("expected text fallback, got: %q", got)
	}
}

func TestSetWidth_rebuildsAndIsIdempotent(t *testing.T) {
	r, err := New(80)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := r.SetWidth(40); err != nil {
		t.Fatalf("SetWidth(40) error = %v", err)
	}
	if r.width != 40 {
		t.Errorf("width = %d, want 40", r.width)
	}
	// Same width: no-op, no error, width unchanged.
	if err := r.SetWidth(40); err != nil {
		t.Fatalf("SetWidth(40) idempotent error = %v", err)
	}
	// Non-positive width: ignored.
	if err := r.SetWidth(0); err != nil {
		t.Fatalf("SetWidth(0) error = %v", err)
	}
	if r.width != 40 {
		t.Errorf("width after no-ops = %d, want 40", r.width)
	}

	// Renderer still works after a width change.
	if _, err := r.Render(model.Item{BodyText: "after resize"}); err != nil {
		t.Errorf("Render after SetWidth error = %v", err)
	}
}

// TestRenderer_concurrentRenderAndSetWidth exercises the renderer from many
// goroutines at once — concurrent Render calls plus a SetWidth racing them. It must
// not data-race (run under -race) and every Render must return content. This guards
// the contract that the UI's preview pane relies on, where a resize can land while a
// body render is still in flight.
func TestRenderer_concurrentRenderAndSetWidth(t *testing.T) {
	r, err := New(80)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	it := model.Item{BodyText: "# Title\n\nSome body text to render across widths."}

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			if i%4 == 0 {
				// Interleave width changes with the renders.
				_ = r.SetWidth(40 + i)
				return
			}
			if out, err := r.Render(it); err != nil || out == "" {
				t.Errorf("concurrent Render: out=%q err=%v", out, err)
			}
		}(i)
	}
	wg.Wait()
}
