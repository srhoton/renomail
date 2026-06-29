//go:build !darwin

package applemail

import (
	"context"
	"errors"
	"testing"
)

// TestMarkReadInMail_unsupportedOffDarwin confirms the non-darwin stub reports
// ErrUnsupported rather than silently dropping the write-back, so SetRead surfaces an
// advisory on platforms without Mail.app / osascript.
func TestMarkReadInMail_unsupportedOffDarwin(t *testing.T) {
	if err := markReadInMail(context.Background(), []string{"a@b"}, true); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("markReadInMail off-darwin = %v, want ErrUnsupported", err)
	}
}
