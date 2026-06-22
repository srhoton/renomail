package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestLoad_missingFile_returnsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.toml")

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if diff := cmp.Diff(Default(), got); diff != "" {
		t.Errorf("Load() mismatch (-want +got):\n%s", diff)
	}
}

func TestSaveLoad_roundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	want := Config{
		SyncInterval: "10m",
		Lookback:     "14d",
		Gmail:        []GmailAccount{{Account: "a@example.com"}},
		OPML:         []OPMLSource{{Path: "~/feeds.opml"}},
		Feed:         []FeedSource{{URL: "https://example.com/rss", Title: "Example"}},
	}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("round-trip mismatch (-want +got):\n%s", diff)
	}
}

func TestLoad_partialFile_appliesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	// Only feeds set; durations omitted should fall back to defaults.
	if err := Save(path, Config{Feed: []FeedSource{{URL: "https://x/rss"}}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.SyncInterval != defaultSyncInterval {
		t.Errorf("SyncInterval = %q, want default %q", got.SyncInterval, defaultSyncInterval)
	}
	if got.Lookback != defaultLookback {
		t.Errorf("Lookback = %q, want default %q", got.Lookback, defaultLookback)
	}
}

func TestLoad_malformedFile_returnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := writeFile(t, path, "this is = not valid = toml ="); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want parse error")
	}
}

func TestLoad_invalidDuration_returnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := writeFile(t, path, "sync_interval = \"bogus\"\n"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want duration validation error")
	}
}

func TestLoad_slackBlock_parses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const body = `
[slack]
webhook_url = "https://hooks.slack.com/services/T/B/X"
max_items = 25
`
	if err := writeFile(t, path, body); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Slack == nil {
		t.Fatal("Slack = nil, want the parsed [slack] table")
	}
	if got.Slack.WebhookURL != "https://hooks.slack.com/services/T/B/X" {
		t.Errorf("WebhookURL = %q", got.Slack.WebhookURL)
	}
	if got.SlackMaxItems() != 25 {
		t.Errorf("SlackMaxItems() = %d, want 25", got.SlackMaxItems())
	}
}

func TestSlackMaxItems_defaults(t *testing.T) {
	// Absent [slack] table.
	if got := (Config{}).SlackMaxItems(); got != defaultSlackMaxItems {
		t.Errorf("SlackMaxItems() with no slack = %d, want %d", got, defaultSlackMaxItems)
	}
	// Present but unset/non-positive max_items.
	if got := (Config{Slack: &SlackConfig{WebhookURL: "https://x"}}).SlackMaxItems(); got != defaultSlackMaxItems {
		t.Errorf("SlackMaxItems() with unset max = %d, want %d", got, defaultSlackMaxItems)
	}
}

func TestLoad_slackWebhookNotHTTPS_returnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := writeFile(t, path, "[slack]\nwebhook_url = \"http://insecure.example/hook\"\n"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want rejection of a non-https webhook")
	}
}

func TestLoad_slackEmptyWebhook_allowed(t *testing.T) {
	// An empty webhook is valid: it may be supplied via RENOMAIL_SLACK_WEBHOOK.
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := writeFile(t, path, "[slack]\nmax_items = 5\n"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil for an empty webhook", err)
	}
	if got.Slack == nil || got.Slack.MaxItems != 5 {
		t.Errorf("Slack = %+v, want the parsed table with max_items=5", got.Slack)
	}
}

func TestSave_unwritableParent_returnsError(t *testing.T) {
	// Place a regular file where Save expects a directory, so MkdirAll fails.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := writeFile(t, blocker, "x"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	target := filepath.Join(blocker, "config.toml")

	if err := Save(target, Default()); err == nil {
		t.Fatal("Save() error = nil, want error when parent is not a directory")
	}
}

func TestSyncEvery(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr bool
	}{
		{name: "explicit minutes", in: "5m", want: 5 * time.Minute},
		{name: "empty applies default", in: "", want: 5 * time.Minute},
		{name: "days suffix", in: "1d", want: 24 * time.Hour},
		{name: "malformed", in: "notaduration", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Config{SyncInterval: tt.in}.SyncEvery()
			if (err != nil) != tt.wantErr {
				t.Fatalf("SyncEvery() err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("SyncEvery() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLookbackDuration(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr bool
	}{
		{name: "days", in: "30d", want: 30 * 24 * time.Hour},
		{name: "empty applies default", in: "", want: 30 * 24 * time.Hour},
		{name: "hours", in: "720h", want: 720 * time.Hour},
		{name: "malformed days", in: "xd", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Config{Lookback: tt.in}.LookbackDuration()
			if (err != nil) != tt.wantErr {
				t.Fatalf("LookbackDuration() err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("LookbackDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNotifyEnabled(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "absent key defaults on", cfg: Config{}, want: true},
		{name: "explicit true", cfg: Config{TmuxNotifications: &yes}, want: true},
		{name: "explicit false", cfg: Config{TmuxNotifications: &no}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.NotifyEnabled(); got != tt.want {
				t.Errorf("NotifyEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoad_tmuxNotifications_parsesOptOut(t *testing.T) {
	tests := []struct {
		name string
		toml string
		want bool
	}{
		{name: "absent stays on", toml: "sync_interval = \"5m\"\n", want: true},
		{name: "explicit false disables", toml: "tmux_notifications = false\n", want: false},
		{name: "explicit true enables", toml: "tmux_notifications = true\n", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := writeFile(t, path, tt.toml); err != nil {
				t.Fatalf("setup: %v", err)
			}
			got, err := Load(path)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got.NotifyEnabled() != tt.want {
				t.Errorf("NotifyEnabled() = %v, want %v", got.NotifyEnabled(), tt.want)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr bool
	}{
		{name: "minutes", in: "5m", want: 5 * time.Minute},
		{name: "hours", in: "720h", want: 720 * time.Hour},
		{name: "two days", in: "2d", want: 48 * time.Hour},
		{name: "fractional days", in: "1.5d", want: 36 * time.Hour},
		{name: "zero days", in: "0d", want: 0},
		{name: "whitespace days", in: " 3d ", want: 72 * time.Hour},
		{name: "bad days", in: "xd", wantErr: true},
		{name: "bad base", in: "10x", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDuration(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseDuration(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseDuration(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
