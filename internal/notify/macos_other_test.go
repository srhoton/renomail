//go:build !darwin

package notify

import (
	"context"
	"errors"
	"testing"
)

// TestMacOS_unsupportedOffDarwin confirms the non-darwin stub reports ErrUnsupported
// rather than silently dropping the alert, so a caller that wires it in off macOS
// surfaces an advisory instead of failing silently.
func TestMacOS_unsupportedOffDarwin(t *testing.T) {
	if err := MacOS(context.Background(), "12 unread emails"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("MacOS off-darwin = %v, want ErrUnsupported", err)
	}
}
