//go:build !darwin

package applemail

import "context"

// markReadInMail reports that read-state write-back is unavailable off macOS (no
// Mail.app / osascript). SetRead returns this rather than silently dropping the change;
// in practice Discover yields no providers off macOS, so it is never reached.
func markReadInMail(_ context.Context, _ []string, _ bool) error {
	return ErrUnsupported
}
