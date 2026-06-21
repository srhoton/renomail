package keys

import (
	"testing"

	"charm.land/bubbles/v2/key"
)

func TestDefault_bindingsAreSet(t *testing.T) {
	k := Default()
	for name, b := range map[string]key.Binding{
		"Up": k.Up, "Down": k.Down, "Top": k.Top, "Bottom": k.Bottom,
		"Open": k.Open, "Back": k.Back, "Quit": k.Quit, "Help": k.Help,
		"OpenBrowser": k.OpenBrowser, "ForceSync": k.ForceSync,
	} {
		if len(b.Keys()) == 0 {
			t.Errorf("binding %q has no keys", name)
		}
	}
}

func TestFullHelp_includesActionBindings(t *testing.T) {
	k := Default()
	want := map[string]bool{"o": false, "R": false} // OpenBrowser, ForceSync
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

func TestKeyMap_helpViewsNonEmpty(t *testing.T) {
	k := Default()
	if len(k.ShortHelp()) == 0 {
		t.Error("ShortHelp() returned no bindings")
	}
	if len(k.FullHelp()) == 0 {
		t.Error("FullHelp() returned no columns")
	}
}
