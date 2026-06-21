package styles

import "testing"

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
