// Package notify delivers notifications out of band. It offers three independent
// channels: Tmux surfaces a transient "new items arrived" message on the host tmux
// status line (local, per-source, fired by the UI); Slack posts a coalesced, richly
// formatted digest of a sweep's new items to a Slack incoming webhook (remote,
// per-sweep, fired by the sync engine); and MacOS posts a Notification Center banner
// when unread counts cross a threshold (local, macOS-only, fired by the sync engine).
// All are optional and best-effort: a delivery failure is returned to the caller to
// surface rather than crashing. The package keeps its dependencies minimal (os/exec
// for tmux and osascript; net/http + encoding/json for Slack) so it stays reusable and
// trivially testable.
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
