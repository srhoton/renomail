// Package notify delivers lightweight, out-of-band notifications to the host
// terminal multiplexer. Currently it targets tmux: when renomail runs inside a
// tmux session, the UI can surface "new items arrived" on the status line without
// stealing focus or switching windows. The package is intentionally tiny and
// dependency-free (only os/exec) so it stays reusable and trivially testable.
package notify

import "os/exec"

// run executes an external command and waits for it to finish. It is a package
// var so tests can swap in a recorder and assert the argv without spawning a real
// process.
var run = func(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

// Tmux shows msg transiently on the current tmux client's status line via
// `tmux display-message`. The caller is responsible for only invoking it inside a
// tmux session (the $TMUX environment variable is set); run outside one the
// underlying tmux command fails and that error is returned for the caller to
// surface rather than crash on. The "--" terminator ensures a message beginning
// with "-" is treated as the message argument, never parsed as a flag.
func Tmux(msg string) error {
	return run("tmux", "display-message", "--", msg)
}
