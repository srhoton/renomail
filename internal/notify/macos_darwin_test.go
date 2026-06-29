//go:build darwin

package notify

import (
	"context"
	"errors"
	"testing"
)

// TestMacOS_passesTitleAndBodyAsArgs swaps the osascript seam so the test asserts the
// exact argv and script without spawning a real osascript: the title and body must be
// passed as process arguments (never interpolated into the script text), and the
// script must be the fixed notifyScript fed on stdin.
func TestMacOS_passesTitleAndBodyAsArgs(t *testing.T) {
	orig := osascriptRun
	t.Cleanup(func() { osascriptRun = orig })

	var gotScript string
	var gotArgs []string
	osascriptRun = func(_ context.Context, script string, args ...string) error {
		gotScript = script
		gotArgs = args
		return nil
	}

	if err := MacOS(context.Background(), "12 unread emails"); err != nil {
		t.Fatalf("MacOS() error = %v", err)
	}
	if gotScript != notifyScript {
		t.Errorf("script = %q, want the fixed notifyScript", gotScript)
	}
	want := []string{macTitle, "12 unread emails"}
	if len(gotArgs) != 2 || gotArgs[0] != want[0] || gotArgs[1] != want[1] {
		t.Errorf("args = %v, want %v (title then body, as argv)", gotArgs, want)
	}
}

// TestMacOS_propagatesError confirms a delivery failure is returned rather than
// swallowed, so the engine can surface it on the status line.
func TestMacOS_propagatesError(t *testing.T) {
	orig := osascriptRun
	t.Cleanup(func() { osascriptRun = orig })

	boom := errors.New("not permitted")
	osascriptRun = func(context.Context, string, ...string) error { return boom }

	if err := MacOS(context.Background(), "x"); !errors.Is(err, boom) {
		t.Fatalf("MacOS() error = %v, want %v", err, boom)
	}
}
