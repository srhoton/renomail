package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// appName is the per-user subdirectory used under the XDG config and data dirs.
const appName = "renomail"

// Paths holds the resolved filesystem locations renomail reads and writes.
// Config and OAuth material live in the config dir; the cache DB and log live in
// the data dir (DESIGN.md §5, §8).
type Paths struct {
	ConfigDir   string // ~/.config/renomail
	DataDir     string // ~/.local/share/renomail
	ConfigFile  string // <ConfigDir>/config.toml
	DBFile      string // <DataDir>/renomail.db
	Credentials string // <ConfigDir>/credentials.json (Gmail OAuth client)
	LogFile     string // <DataDir>/renomail.log
}

// ResolvePaths computes the application paths, honoring XDG_CONFIG_HOME and
// XDG_DATA_HOME when set and falling back to the platform defaults otherwise.
func ResolvePaths() (Paths, error) {
	configBase, err := configBaseDir()
	if err != nil {
		return Paths{}, err
	}
	dataBase, err := dataBaseDir()
	if err != nil {
		return Paths{}, err
	}

	configDir := filepath.Join(configBase, appName)
	dataDir := filepath.Join(dataBase, appName)

	return Paths{
		ConfigDir:   configDir,
		DataDir:     dataDir,
		ConfigFile:  filepath.Join(configDir, "config.toml"),
		DBFile:      filepath.Join(dataDir, "renomail.db"),
		Credentials: filepath.Join(configDir, "credentials.json"),
		LogFile:     filepath.Join(dataDir, "renomail.log"),
	}, nil
}

// configBaseDir returns XDG_CONFIG_HOME or ~/.config. It mirrors dataBaseDir's
// XDG-style default rather than os.UserConfigDir() — which resolves to
// ~/Library/Application Support on macOS — so the documented ~/.config/renomail
// location is honored on every platform (DESIGN.md §8, docs/CONFIG.md).
func configBaseDir() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config"), nil
}

// dataBaseDir returns XDG_DATA_HOME or ~/.local/share.
func dataBaseDir() (string, error) {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".local", "share"), nil
}

// ExpandTilde replaces a leading "~" with the current user's home directory.
// Paths that do not start with "~" are returned unchanged.
func ExpandTilde(p string) (string, error) {
	if p != "~" && !hasTildePrefix(p) {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %q: %w", p, err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}

// hasTildePrefix reports whether p begins with "~/".
func hasTildePrefix(p string) bool {
	return len(p) >= 2 && p[0] == '~' && p[1] == '/'
}

// TokenFile returns the OAuth token path for a Gmail account. The account is
// sanitized first so a malformed value (containing path separators or traversal
// sequences) cannot escape ConfigDir.
func (p Paths) TokenFile(account string) string {
	return filepath.Join(p.ConfigDir, "token-"+sanitizeAccount(account)+".json")
}

// sanitizeAccount strips path separators and traversal components from an account
// so it is safe to embed in a filename. A value that reduces to nothing usable
// becomes "_".
func sanitizeAccount(account string) string {
	cleaned := strings.ReplaceAll(account, "/", "_")
	cleaned = strings.ReplaceAll(cleaned, `\`, "_")
	cleaned = filepath.Base(cleaned) // drops any residual separators / traversal
	switch cleaned {
	case "", ".", "..":
		return "_"
	default:
		return cleaned
	}
}
