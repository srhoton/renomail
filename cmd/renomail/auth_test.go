package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/srhoton/renomail/internal/config"
)

func TestAuthAccount(t *testing.T) {
	if got := authAccount(nil); got != "" {
		t.Errorf("authAccount(nil) = %q, want empty", got)
	}
	if got := authAccount([]string{"me@example.com", "extra"}); got != "me@example.com" {
		t.Errorf("authAccount = %q", got)
	}
}

func TestRunAuth_noAccount_returnsUsage(t *testing.T) {
	if err := runAuth(context.Background(), config.Paths{}, ""); !errors.Is(err, errAuthUsage) {
		t.Errorf("runAuth(\"\") err = %v, want errAuthUsage", err)
	}
}

func TestRunAuth_missingCredentials_returnsError(t *testing.T) {
	paths := config.Paths{
		ConfigDir:   t.TempDir(),
		Credentials: filepath.Join(t.TempDir(), "absent.json"),
	}
	// No credentials file, so Authorize fails before any browser/loopback work.
	err := runAuth(context.Background(), paths, "me@example.com")
	if err == nil || errors.Is(err, errAuthUsage) {
		t.Errorf("runAuth err = %v, want a credentials read error", err)
	}
}

// TestDispatch_auth_routesToRunAuth verifies dispatch sends `auth <account>` to
// runAuth: with a config dir but no credentials.json, Authorize fails on the
// credentials read, proving the route was taken (not the TUI).
func TestDispatch_auth_routesToRunAuth(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(cfgHome, "renomail"), 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := dispatch(context.Background(), []string{"auth", "me@example.com"}, os.Stdout)
	if err == nil {
		t.Fatal("dispatch(auth) err = nil, want credentials error")
	}
	if errors.Is(err, errAuthUsage) {
		t.Errorf("err = %v, want a credentials error (account was provided)", err)
	}
}

func TestDispatch_auth_noAccount_returnsUsage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	err := dispatch(context.Background(), []string{"auth"}, os.Stdout)
	if !errors.Is(err, errAuthUsage) {
		t.Errorf("dispatch(auth) err = %v, want errAuthUsage", err)
	}
}
