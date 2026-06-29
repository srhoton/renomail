package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/srhoton/renomail/internal/config"
	"github.com/srhoton/renomail/internal/notify"
	"github.com/srhoton/renomail/internal/source/applemail"
	"github.com/srhoton/renomail/internal/source/rss"
	"github.com/srhoton/renomail/internal/store"
	"github.com/srhoton/renomail/internal/syncengine"
	"github.com/srhoton/renomail/internal/ui"
)

// runTUI builds the program, starts the background sync engine, and launches the
// Bubble Tea program. It owns the engine's context: cancelling it on exit lets
// Run close its events channel so the UI's listener unblocks cleanly. The
// alternate screen is enabled on the root model's View (the v2 replacement for the
// WithAltScreen program option).
func runTUI(cfg config.Config, paths config.Paths, opts ...tea.ProgramOption) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m, st, eng, err := buildTUI(ctx, cfg, paths)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	go eng.Run(ctx)

	_, runErr := tea.NewProgram(m, opts...).Run()

	// Stop the engine before releasing the shared Apple Mail snapshot so a sweep is
	// not still acquiring it as we close. We do not join the engine goroutine here: its
	// shutdown can block up to the digest timeout, which must not stall quitting.
	cancel()
	applemail.CloseCache()

	if runErr != nil {
		return fmt.Errorf("run tui: %w", runErr)
	}
	return nil
}

// buildTUI performs the testable wiring behind runTUI: ensure the data dir, open
// the store, build the provider set, construct the sync engine, and build the root
// model wired to the engine's events. It is separated from the program launch
// (which needs a real terminal) and the engine goroutine so the setup path can be
// unit-tested. On any failure after the store opens, the store is closed before
// returning.
func buildTUI(ctx context.Context, cfg config.Config, paths config.Paths) (ui.Model, *store.Store, *syncengine.Engine, error) {
	// Mirror runDump: the data dir may not exist on a first run, and the SQLite
	// driver will not create it.
	if err := os.MkdirAll(paths.DataDir, 0o700); err != nil {
		return ui.Model{}, nil, nil, fmt.Errorf("create data dir %s: %w", paths.DataDir, err)
	}
	st, err := store.Open(paths.DBFile)
	if err != nil {
		return ui.Model{}, nil, nil, err
	}

	client := &http.Client{Timeout: rss.DefaultTimeout}
	providers, warns, err := buildProviders(ctx, cfg, paths, st, client)
	if err != nil {
		_ = st.Close()
		return ui.Model{}, nil, nil, err
	}

	interval, err := cfg.SyncEvery()
	if err != nil {
		_ = st.Close()
		return ui.Model{}, nil, nil, err
	}

	eng := syncengine.New(providers, st, interval)
	m, err := ui.New(st, eng.Events(), providers, eng.Trigger, warns)
	if err != nil {
		_ = st.Close()
		return ui.Model{}, nil, nil, err
	}
	// When running inside tmux (and not disabled in config), surface new items on
	// the tmux status line as they arrive. Detection is automatic via $TMUX; the
	// env read lives here in the cmd layer so the ui package stays pure.
	if os.Getenv("TMUX") != "" && cfg.NotifyEnabled() {
		m.SetNotifier(notify.Tmux)
	}
	// When a Slack webhook is configured, post a coalesced per-sweep digest of new
	// items. The env var takes precedence so the secret can stay out of the config
	// file; the env read lives in the cmd layer to keep the engine/config pure. The
	// https check mirrors config.Load's validation so an env-supplied webhook is held
	// to the same standard (config-file webhooks are already validated at load).
	if webhook := slackWebhook(cfg); webhook != "" {
		if !strings.HasPrefix(webhook, "https://") {
			_ = st.Close()
			return ui.Model{}, nil, nil, fmt.Errorf("slack webhook must be an https URL")
		}
		eng.SetDigestNotifier(notify.NewSlack(webhook, cfg.SlackMaxItems()).Notify)
	}
	// On macOS, post a Notification Center banner when unread counts cross a threshold,
	// so a backlog surfaces on the desktop even outside tmux. On by default; the
	// runtime.GOOS gate keeps the off-darwin stub (which returns ErrUnsupported) from
	// ever being wired in.
	if cfg.MacNotifyEnabled() && runtime.GOOS == "darwin" {
		eng.SetThresholdNotifier(notify.MacOS)
	}
	return m, st, eng, nil
}

// slackWebhook resolves the Slack incoming-webhook URL, preferring the
// RENOMAIL_SLACK_WEBHOOK environment variable over the config file so the secret
// need not be written to disk. Returns "" when Slack is not configured.
func slackWebhook(cfg config.Config) string {
	if env := os.Getenv("RENOMAIL_SLACK_WEBHOOK"); env != "" {
		return env
	}
	if cfg.Slack != nil {
		return cfg.Slack.WebhookURL
	}
	return ""
}
