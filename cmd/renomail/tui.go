package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/srhoton/renomail/internal/config"
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

	if _, err := tea.NewProgram(m, opts...).Run(); err != nil {
		return fmt.Errorf("run tui: %w", err)
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
	m, err := ui.New(st, eng.Events(), providers, warns)
	if err != nil {
		_ = st.Close()
		return ui.Model{}, nil, nil, err
	}
	return m, st, eng, nil
}
