package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a small shared test helper.
func writeFile(t *testing.T, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestResolvePaths_honorsXDGOverrides(t *testing.T) {
	configHome := t.TempDir()
	dataHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)

	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths() error = %v", err)
	}

	wantConfigDir := filepath.Join(configHome, "renomail")
	wantDataDir := filepath.Join(dataHome, "renomail")
	checks := map[string][2]string{
		"ConfigDir":   {p.ConfigDir, wantConfigDir},
		"DataDir":     {p.DataDir, wantDataDir},
		"ConfigFile":  {p.ConfigFile, filepath.Join(wantConfigDir, "config.toml")},
		"DBFile":      {p.DBFile, filepath.Join(wantDataDir, "renomail.db")},
		"Credentials": {p.Credentials, filepath.Join(wantConfigDir, "credentials.json")},
		"LogFile":     {p.LogFile, filepath.Join(wantDataDir, "renomail.log")},
	}
	for field, got := range checks {
		if got[0] != got[1] {
			t.Errorf("%s = %q, want %q", field, got[0], got[1])
		}
	}
}

func TestResolvePaths_fallsBackWithoutXDG(t *testing.T) {
	// Empty XDG vars force the ~/.config and ~/.local/share defaults — notably NOT
	// os.UserConfigDir() (~/Library/Application Support on macOS), so the documented
	// ~/.config/renomail path is honored on every platform.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths() error = %v", err)
	}
	wantConfigSuffix := filepath.Join(".config", "renomail")
	if !strings.HasSuffix(p.ConfigDir, wantConfigSuffix) {
		t.Errorf("ConfigDir = %q, want suffix %q", p.ConfigDir, wantConfigSuffix)
	}
	wantDataSuffix := filepath.Join(".local", "share", "renomail")
	if !strings.HasSuffix(p.DataDir, wantDataSuffix) {
		t.Errorf("DataDir = %q, want suffix %q", p.DataDir, wantDataSuffix)
	}
}

func TestTokenFile(t *testing.T) {
	p := Paths{ConfigDir: "/cfg"}
	got := p.TokenFile("user@example.com")
	want := filepath.Join("/cfg", "token-user@example.com.json")
	if got != want {
		t.Errorf("TokenFile() = %q, want %q", got, want)
	}
}

func TestTokenFile_sanitizesAccount(t *testing.T) {
	p := Paths{ConfigDir: "/cfg"}
	tests := []struct {
		name    string
		account string
	}{
		{name: "forward slash", account: "../evil"},
		{name: "absolute", account: "/etc/passwd"},
		{name: "backslash", account: `..\evil`},
		{name: "dot dot", account: ".."},
		{name: "empty", account: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.TokenFile(tt.account)
			// The result must stay directly inside ConfigDir (no traversal).
			if dir := filepath.Dir(got); dir != p.ConfigDir {
				t.Errorf("TokenFile(%q) escaped ConfigDir: dir = %q, want %q", tt.account, dir, p.ConfigDir)
			}
		})
	}
}

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "tilde slash path", in: "~/feeds.opml", want: filepath.Join(home, "feeds.opml")},
		{name: "bare tilde", in: "~", want: home},
		{name: "absolute passthrough", in: "/etc/renomail.toml", want: "/etc/renomail.toml"},
		{name: "relative passthrough", in: "feeds.opml", want: "feeds.opml"},
		{name: "tilde in middle untouched", in: "/a/~/b", want: "/a/~/b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandTilde(tt.in)
			if err != nil {
				t.Fatalf("ExpandTilde(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ExpandTilde(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
