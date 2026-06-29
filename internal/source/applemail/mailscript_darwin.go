//go:build darwin

package applemail

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// markReadScript sets the read status of every inbox message whose `message id`
// matches one of the ids passed as argv. The ids arrive as process arguments (read via
// `on run argv`), never interpolated into the script text, so an adversarial
// Message-ID cannot inject AppleScript. argv item 1 is the target read status
// ("true"/"false"); items 2.. are bare Message-IDs. Mail's `inbox` is the unified
// inbox across accounts, which for a Gmail account includes its All Mail messages that
// carry the INBOX label. A per-id `try` keeps one unmatched id from aborting the rest.
const markReadScript = `on run argv
	set wantRead to (item 1 of argv is "true")
	tell application "Mail"
		repeat with i from 2 to count of argv
			try
				set theMatches to (messages of inbox whose message id is (item i of argv))
				repeat with m in theMatches
					set read status of m to wantRead
				end repeat
			end try
		end repeat
	end tell
end run`

// markReadInMail invokes osascript to set the read status of the given messages in
// Mail.app. It runs `osascript - <wantRead> <id>...`: reading the script from stdin
// with "-" lets the trailing arguments reach the script's `on run argv` handler, so the
// ids are passed as data, not concatenated into the program. osascript launches
// Mail.app if it is not already running.
func markReadInMail(ctx context.Context, msgIDs []string, read bool) error {
	// Absolute path (not a PATH lookup) so a planted "osascript" earlier in PATH cannot
	// be run in place of the system interpreter.
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", osascriptArgs(msgIDs, read)...)
	cmd.Stdin = strings.NewReader(markReadScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apple mail set read via osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
