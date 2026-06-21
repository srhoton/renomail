package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDispatch_firstRun_printsZeroCounts(t *testing.T) {
	// Empty XDG dirs => no config file => first-run path.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	var buf bytes.Buffer
	if err := dispatch(context.Background(), nil, &buf); err != nil {
		t.Fatalf("dispatch() error = %v", err)
	}
	want := "renomail: 0 gmail account(s), 0 opml file(s), 0 feed(s)\n"
	if got := buf.String(); got != want {
		t.Errorf("dispatch() output = %q, want %q", got, want)
	}
}

func TestDispatch_withConfig_printsCounts(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	dir := filepath.Join(cfgHome, "renomail")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	content := "[[gmail]]\naccount = \"a@example.com\"\n" +
		"[[opml]]\npath = \"x.opml\"\n" +
		"[[feed]]\nurl = \"https://example.com/rss\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var buf bytes.Buffer
	if err := dispatch(context.Background(), nil, &buf); err != nil {
		t.Fatalf("dispatch() error = %v", err)
	}
	want := "renomail: 1 gmail account(s), 1 opml file(s), 1 feed(s)\n"
	if got := buf.String(); got != want {
		t.Errorf("dispatch() output = %q, want %q", got, want)
	}
}

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
