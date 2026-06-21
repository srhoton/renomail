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
	} {
		if len(b.Keys()) == 0 {
			t.Errorf("binding %q has no keys", name)
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
