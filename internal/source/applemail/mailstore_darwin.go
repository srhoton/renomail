//go:build darwin

package applemail

import (
	"fmt"
	"os"
	"path/filepath"
)

// defaultMailRoot returns ~/Library/Mail, the root of Apple Mail's on-disk store on
// macOS. Reading under it requires Full Disk Access for the host terminal.
func defaultMailRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("apple mail: resolve home dir: %w", err)
	}
	return filepath.Join(home, "Library", "Mail"), nil
}
