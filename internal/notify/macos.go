package notify

import (
	"context"
	"errors"
)

// ErrUnsupported is returned by MacOS off macOS, where there is no Notification
// Center / osascript to post to. The caller (the sync engine) only wires MacOS in on
// darwin, so in practice this is never reached at runtime.
var ErrUnsupported = errors.New("macos notifications unavailable off macOS")

// macTitle is the banner title used for every renomail Notification Center alert.
const macTitle = "renomail"

// MacOS posts msg as a macOS Notification Center banner titled "renomail". It is
// best-effort: a delivery failure is returned for the caller to surface rather than
// crash on. The context bounds the underlying osascript invocation. Off macOS it
// returns ErrUnsupported.
func MacOS(ctx context.Context, msg string) error {
	return displayNotification(ctx, macTitle, msg)
}
