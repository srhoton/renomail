//go:build !darwin

package applemail

import (
	"context"
	"errors"
	"testing"
	"time"
)

// On non-macOS builds the source is unavailable: Discover reports ErrUnsupported so
// the builder can degrade to a single advisory warning while the rest of renomail
// builds and runs unchanged.
func TestDiscover_unsupportedOffDarwin(t *testing.T) {
	_, err := Discover(context.Background(), 30*24*time.Hour)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Discover() error = %v, want ErrUnsupported", err)
	}
}
