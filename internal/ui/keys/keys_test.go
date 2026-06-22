package keys

import (
	"slices"
	"testing"

	"charm.land/bubbles/v2/key"
)

func TestDefault_bindingsAreSet(t *testing.T) {
	k := Default()
	for name, b := range map[string]key.Binding{
		"Up": k.Up, "Down": k.Down, "Top": k.Top, "Bottom": k.Bottom,
		"Open": k.Open, "TogglePane": k.TogglePane, "Back": k.Back, "Quit": k.Quit, "Help": k.Help,
		"OpenBrowser": k.OpenBrowser, "ForceSync": k.ForceSync,
		"CycleRead": k.CycleRead, "MarkSourceRead": k.MarkSourceRead,
	} {
		if len(b.Keys()) == 0 {
			t.Errorf("binding %q has no keys", name)
		}
	}
}

func TestFullHelp_includesActionBindings(t *testing.T) {
	k := Default()
	// OpenBrowser, ForceSync, CycleRead, MarkSourceRead
	want := map[string]bool{"o": false, "R": false, "u": false, "S": false}
	for _, col := range k.FullHelp() {
		for _, b := range col {
			for _, key := range b.Keys() {
				if _, ok := want[key]; ok {
					want[key] = true
				}
			}
		}
	}
	for keyName, found := range want {
		if !found {
			t.Errorf("FullHelp() is missing the %q binding", keyName)
		}
	}
}

func TestDefault_togglePaneBoundToP(t *testing.T) {
	k := Default()
	if !bindingHasKey(k.TogglePane, "p") {
		t.Errorf("TogglePane not bound to %q; keys = %v", "p", k.TogglePane.Keys())
	}
	// The preview binding must surface in both help views so it is discoverable.
	if !bindingsHaveKey(k.ShortHelp(), "p") {
		t.Error("ShortHelp() is missing the preview (p) binding")
	}
	var full []key.Binding
	for _, col := range k.FullHelp() {
		full = append(full, col...)
	}
	if !bindingsHaveKey(full, "p") {
		t.Error("FullHelp() is missing the preview (p) binding")
	}
}

// bindingHasKey reports whether b binds the given key string.
func bindingHasKey(b key.Binding, want string) bool {
	return slices.Contains(b.Keys(), want)
}

// bindingsHaveKey reports whether any binding in the slice binds the given key string.
func bindingsHaveKey(bs []key.Binding, want string) bool {
	for _, b := range bs {
		if bindingHasKey(b, want) {
			return true
		}
	}
	return false
}

func TestKeyMap_helpViewsNonEmpty(t *testing.T) {
	k := Default()
	if len(k.ShortHelp()) == 0 {
		t.Error("ShortHelp() returned no bindings")
	}
	if len(k.FullHelp()) == 0 {
		t.Error("FullHelp() returned no columns")
	}
}
