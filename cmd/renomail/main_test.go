package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRun_firstRun_printsZeroCounts(t *testing.T) {
	// Empty XDG dirs => no config file => first-run path.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	var buf bytes.Buffer
	if err := run(&buf); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	want := "renomail: 0 gmail account(s), 0 opml file(s), 0 feed(s)\n"
	if got := buf.String(); got != want {
		t.Errorf("run() output = %q, want %q", got, want)
	}
}

func TestRun_withConfig_printsCounts(t *testing.T) {
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
	if err := run(&buf); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	want := "renomail: 1 gmail account(s), 1 opml file(s), 1 feed(s)\n"
	if got := buf.String(); got != want {
		t.Errorf("run() output = %q, want %q", got, want)
	}
}
