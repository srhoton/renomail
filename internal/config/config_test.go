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
