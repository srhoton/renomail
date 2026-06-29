//go:build darwin

package notify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// notifyScript posts a Notification Center banner whose title and body arrive as
// argv (read via `on run argv`), never interpolated into the script text, so an
// adversarial title/body cannot inject AppleScript. argv item 1 is the title; item 2
// is the body.
const notifyScript = `on run argv
	display notification (item 2 of argv) with title (item 1 of argv)
end run`

// osascriptRun executes notifyScript via osascript, passing title and body as
// process arguments. It is a package var so tests can swap in a recorder and assert
// the argv (and that the script is fed on stdin) without spawning a real osascript.
var osascriptRun = func(ctx context.Context, script string, args ...string) error {
	// Absolute path (not a PATH lookup) so a planted "osascript" earlier in PATH
	// cannot be run in place of the system interpreter. "-" reads the script from
	// stdin, letting the trailing arguments reach the script's `on run argv` handler
	// as data rather than being concatenated into the program.
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", append([]string{"-"}, args...)...)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("display notification via osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// displayNotification posts a banner with the given title and body.
func displayNotification(ctx context.Context, title, body string) error {
	return osascriptRun(ctx, notifyScript, title, body)
}
