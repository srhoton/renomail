//go:build !darwin

package notify

import "context"

// displayNotification reports that Notification Center is unavailable off macOS (no
// osascript). MacOS returns this rather than silently dropping the alert; in practice
// the sync engine only wires MacOS in on darwin, so it is never reached.
func displayNotification(_ context.Context, _, _ string) error {
	return ErrUnsupported
}
