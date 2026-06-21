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
