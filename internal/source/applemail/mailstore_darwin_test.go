//go:build darwin

package applemail

import (
	"path/filepath"
	"testing"
)

// defaultMailRoot only builds the ~/Library/Mail path; it performs no IO, so this is
// hermetic and never touches real mail.
func TestDefaultMailRoot(t *testing.T) {
	root, err := defaultMailRoot()
	if err != nil {
		t.Fatalf("defaultMailRoot: %v", err)
	}
	if want := filepath.Join("Library", "Mail"); filepath.Base(filepath.Dir(root)) != "Library" || filepath.Base(root) != "Mail" {
		t.Errorf("defaultMailRoot = %q, want it to end in %q", root, want)
	}
}
