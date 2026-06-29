package applemail

import (
	"slices"
	"testing"
)

func TestMessageIDsForMail(t *testing.T) {
	in := []string{
		"<abc@example.com>", // angle brackets stripped
		"plain@id.com",      // already bare
		"  <spaced@x>  ",    // trimmed then unwrapped
		"rowid:42",          // no header — dropped
		"",                  // blank — dropped
		"<>",                // empty inner — dropped
	}
	got := messageIDsForMail(in)
	want := []string{"abc@example.com", "plain@id.com", "spaced@x"}
	if !slices.Equal(got, want) {
		t.Errorf("messageIDsForMail(%v) = %v, want %v", in, got, want)
	}
}

func TestOsascriptArgs_passesIdsAsArgvNotScript(t *testing.T) {
	ids := []string{"abc@x", "def@y"}
	got := osascriptArgs(ids, true)
	want := []string{"-", "true", "abc@x", "def@y"}
	if !slices.Equal(got, want) {
		t.Fatalf("osascriptArgs read=true = %v, want %v", got, want)
	}
	// The script is read from stdin ("-"); ids ride in as later argv items, never
	// interpolated into the program text, so a crafted Message-ID cannot inject.
	if got[0] != "-" {
		t.Errorf("first arg = %q, want %q (script from stdin)", got[0], "-")
	}

	if got := osascriptArgs([]string{"m@x"}, false); !slices.Equal(got, []string{"-", "false", "m@x"}) {
		t.Errorf("osascriptArgs read=false = %v, want [- false m@x]", got)
	}
}

func TestOsascriptArgs_emptyIDs(t *testing.T) {
	got := osascriptArgs(nil, true)
	if !slices.Equal(got, []string{"-", "true"}) {
		t.Errorf("osascriptArgs(nil) = %v, want [- true]", got)
	}
}

// TestOsascriptArgs_leadingDashIDStaysArgv pins the injection-safety invariant: a
// Message-ID beginning with '-' must remain positional argv (osascript ends option
// parsing at the leading "-" stdin sentinel), never be misparsed as a flag. Guards the
// arg order against future refactors.
func TestOsascriptArgs_leadingDashIDStaysArgv(t *testing.T) {
	got := osascriptArgs([]string{"-e@evil.com"}, true)
	want := []string{"-", "true", "-e@evil.com"}
	if !slices.Equal(got, want) {
		t.Fatalf("osascriptArgs = %v, want %v", got, want)
	}
	if got[0] != "-" || got[1] != "true" {
		t.Errorf("every id must follow the - and status args; got %v", got)
	}
}
