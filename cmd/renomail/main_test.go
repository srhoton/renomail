package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/srhoton/renomail/internal/config"
)

// TestDispatch_dump_noFeeds exercises the dump wiring (dispatch -> runDump ->
// store.Open -> BuildRSSProviders -> dumpFeeds) with an empty config, so it runs
// without network: zero providers, an empty store, no output.
func TestDispatch_dump_noFeeds(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	var buf bytes.Buffer
	if err := dispatch(context.Background(), []string{"dump"}, &buf); err != nil {
		t.Fatalf("dispatch(dump) error = %v", err)
	}
	if got := buf.String(); got != "" {
		t.Errorf("dispatch(dump) output = %q, want empty", got)
	}
}

// TestDispatch_invalidConfig_errors covers the config.Load error branch.
func TestDispatch_invalidConfig_errors(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	dir := filepath.Join(cfgHome, "renomail")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("sync_interval = \"not-a-duration\"\n"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := dispatch(context.Background(), nil, &bytes.Buffer{}); err == nil {
		t.Fatal("dispatch() error = nil, want error for invalid config")
	}
}

// TestDispatch_dump_badOPML covers runDump's provider-build error branch.
func TestDispatch_dump_badOPML(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	dir := filepath.Join(cfgHome, "renomail")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	content := "[[opml]]\npath = \"" + filepath.Join(dir, "missing.opml") + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := dispatch(context.Background(), []string{"dump"}, &bytes.Buffer{}); err == nil {
		t.Fatal("dispatch(dump) error = nil, want error for missing OPML file")
	}
}

// TestRun_dump_returnsZero covers the run() wrapper success path via the dump
// subcommand (no network, empty config).
func TestRun_dump_returnsZero(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if code := run([]string{"dump"}, &bytes.Buffer{}); code != 0 {
		t.Errorf("run(dump) = %d, want 0", code)
	}
}

// TestRun_invalidConfig_returnsOne covers run()'s error-reporting branch.
func TestRun_invalidConfig_returnsOne(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	dir := filepath.Join(cfgHome, "renomail")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("sync_interval = \"nope\"\n"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if code := run(nil, &bytes.Buffer{}); code != 1 {
		t.Errorf("run(invalid) = %d, want 1", code)
	}
}

// TestRunTUI_dataDirError covers runTUI's pre-launch error path: when the data
// directory cannot be created the program must never start. Pointing DataDir
// beneath a regular file makes MkdirAll fail deterministically.
func TestRunTUI_dataDirError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	paths := config.Paths{
		DataDir: filepath.Join(blocker, "data"), // parent is a file -> MkdirAll fails
		DBFile:  filepath.Join(blocker, "data", "renomail.db"),
	}
	if err := runTUI(config.Config{}, paths); err == nil {
		t.Fatal("runTUI() error = nil, want error when data dir cannot be created")
	}
}

// TestBuildTUI_success covers the happy-path wiring behind runTUI: data dir
// created, store opened, root model built. It stops short of launching the
// program (which needs a real terminal).
func TestBuildTUI_success(t *testing.T) {
	dir := t.TempDir()
	paths := config.Paths{
		DataDir: filepath.Join(dir, "data"),
		DBFile:  filepath.Join(dir, "data", "renomail.db"),
	}
	_, st, eng, err := buildTUI(context.Background(), config.Config{}, paths)
	if err != nil {
		t.Fatalf("buildTUI() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if st == nil {
		t.Fatal("buildTUI() store = nil, want open store")
	}
	if eng == nil {
		t.Fatal("buildTUI() engine = nil, want a constructed sync engine")
	}
}

func TestBuildTUI_withSlackConfig_succeeds(t *testing.T) {
	dir := t.TempDir()
	paths := config.Paths{
		DataDir: filepath.Join(dir, "data"),
		DBFile:  filepath.Join(dir, "data", "renomail.db"),
	}
	cfg := config.Config{Slack: &config.SlackConfig{WebhookURL: "https://hooks.slack.com/services/T/B/X"}}

	_, st, eng, err := buildTUI(context.Background(), cfg, paths)
	if err != nil {
		t.Fatalf("buildTUI() with slack config error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if eng == nil {
		t.Fatal("buildTUI() engine = nil, want a constructed sync engine")
	}
}

func TestBuildTUI_appleMailEnabled_succeeds(t *testing.T) {
	dir := t.TempDir()
	paths := config.Paths{
		DataDir: filepath.Join(dir, "data"),
		DBFile:  filepath.Join(dir, "data", "renomail.db"),
	}
	// Enable Apple Mail but point HOME at an empty tree so discovery finds nothing:
	// the wiring runs without needing Full Disk Access or real Apple Mail data.
	t.Setenv("HOME", filepath.Join(dir, "home"))
	cfg := config.Config{AppleMail: &config.AppleMailConfig{Enabled: true}}

	_, st, eng, err := buildTUI(context.Background(), cfg, paths)
	if err != nil {
		t.Fatalf("buildTUI() with apple mail enabled error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if eng == nil {
		t.Fatal("buildTUI() engine = nil, want a constructed sync engine")
	}
}

func TestBuildTUI_nonHTTPSEnvWebhook_errors(t *testing.T) {
	dir := t.TempDir()
	paths := config.Paths{
		DataDir: filepath.Join(dir, "data"),
		DBFile:  filepath.Join(dir, "data", "renomail.db"),
	}
	// A non-https webhook supplied via env must be rejected just like a config-file one.
	t.Setenv("RENOMAIL_SLACK_WEBHOOK", "http://insecure.example/hook")

	_, _, _, err := buildTUI(context.Background(), config.Config{}, paths)
	if err == nil {
		t.Fatal("buildTUI() = nil error, want rejection of a non-https env webhook")
	}
}

func TestSlackWebhook_resolution(t *testing.T) {
	cfg := config.Config{Slack: &config.SlackConfig{WebhookURL: "https://config.example/hook"}}

	t.Run("env overrides config", func(t *testing.T) {
		t.Setenv("RENOMAIL_SLACK_WEBHOOK", "https://env.example/hook")
		if got := slackWebhook(cfg); got != "https://env.example/hook" {
			t.Errorf("slackWebhook() = %q, want the env value", got)
		}
	})
	t.Run("falls back to config", func(t *testing.T) {
		t.Setenv("RENOMAIL_SLACK_WEBHOOK", "")
		if got := slackWebhook(cfg); got != "https://config.example/hook" {
			t.Errorf("slackWebhook() = %q, want the config value", got)
		}
	})
	t.Run("empty when unconfigured", func(t *testing.T) {
		t.Setenv("RENOMAIL_SLACK_WEBHOOK", "")
		if got := slackWebhook(config.Config{}); got != "" {
			t.Errorf("slackWebhook() = %q, want empty", got)
		}
	})
}

// TestRunTUI_runsAndQuits drives the full launch path headlessly: it feeds a "q"
// keypress through an in-memory input and discards output, so the program starts,
// processes the quit binding, and returns without a terminal.
func TestRunTUI_runsAndQuits(t *testing.T) {
	dir := t.TempDir()
	paths := config.Paths{
		DataDir: filepath.Join(dir, "data"),
		DBFile:  filepath.Join(dir, "data", "renomail.db"),
	}
	err := runTUI(config.Config{}, paths,
		tea.WithInput(strings.NewReader("q")),
		tea.WithOutput(io.Discard),
	)
	if err != nil {
		t.Fatalf("runTUI() error = %v", err)
	}
}
