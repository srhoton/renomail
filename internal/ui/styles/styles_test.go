package styles

import (
	"testing"

	"charm.land/lipgloss/v2"
)

// unsetBackground is what GetBackground returns for a style with no background
// set; comparing against it is more robust than assuming a nil sentinel. This
// relies on lipgloss's color.Color implementations being comparable value types
// (today NoColor{} when unset and an indexed color when set), so == is safe and
// won't panic; if lipgloss ever returns a pointer/slice-backed color these
// comparisons would need a stable string form instead.
var unsetBackground = lipgloss.NewStyle().GetBackground()

func TestDefaultStyles_unreadDistinctFromRead(t *testing.T) {
	s := DefaultStyles()

	// Unread rows are bold; read rows are faint. They must differ so the feed
	// visibly separates new items from seen ones.
	if !s.Unread.GetBold() {
		t.Error("Unread style is not bold")
	}
	if !s.Read.GetFaint() {
		t.Error("Read style is not faint")
	}
	if s.Unread.GetBold() == s.Read.GetBold() && s.Unread.GetFaint() == s.Read.GetFaint() {
		t.Error("Unread and Read styles are indistinguishable")
	}

	// Rendering must not panic and must echo the content.
	if got := s.Header.Render("hi"); got == "" {
		t.Error("Header.Render returned empty output")
	}
}

func TestStylesForBackground_selectionBackgroundPerBackground(t *testing.T) {
	light := stylesForBackground(false)
	dark := stylesForBackground(true)

	// The selection bar carries a background, chosen per terminal background so it
	// stands out on both. Both must set one, and they must differ.
	if light.Selected.GetBackground() == unsetBackground {
		t.Error("light: Selected has no background")
	}
	if dark.Selected.GetBackground() == unsetBackground {
		t.Error("dark: Selected has no background")
	}
	if light.Selected.GetBackground() == dark.Selected.GetBackground() {
		t.Error("selection background is identical for light and dark backgrounds")
	}
}

func TestRowStyle_composesEmphasisAndSelection(t *testing.T) {
	s := DefaultStyles()
	selBg := s.Selected.GetBackground()

	tests := []struct {
		name           string
		read, selected bool
		wantBold       bool
		wantFaint      bool
	}{
		{"unread unselected", false, false, true, false},
		{"read unselected", true, false, false, true},
		{"unread selected", false, true, true, false},
		{"read selected", true, true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.RowStyle(tt.read, tt.selected)
			if got.GetBold() != tt.wantBold {
				t.Errorf("Bold = %v, want %v", got.GetBold(), tt.wantBold)
			}
			if got.GetFaint() != tt.wantFaint {
				t.Errorf("Faint = %v, want %v", got.GetFaint(), tt.wantFaint)
			}
			// The selection background is present only on the cursor row.
			if tt.selected {
				if got.GetBackground() != selBg {
					t.Errorf("selected row background = %v, want the selection bg %v", got.GetBackground(), selBg)
				}
			} else if got.GetBackground() != unsetBackground {
				t.Errorf("unselected row has background %v, want none", got.GetBackground())
			}
		})
	}
}

func TestStylesForBackground_pickHeaderPerBackground(t *testing.T) {
	light := stylesForBackground(false)
	dark := stylesForBackground(true)

	// The header accent is chosen per background for contrast, so the two palettes
	// must not share a foreground; the bold/faint distinctions stay constant.
	if light.Header.GetForeground() == dark.Header.GetForeground() {
		t.Error("header foreground is identical for light and dark backgrounds")
	}
	for name, s := range map[string]Styles{"light": light, "dark": dark} {
		if !s.Unread.GetBold() {
			t.Errorf("%s: Unread is not bold", name)
		}
		if !s.Read.GetFaint() {
			t.Errorf("%s: Read is not faint", name)
		}
		if got := s.Header.Render("hi"); got == "" {
			t.Errorf("%s: Header.Render returned empty output", name)
		}
	}
}
