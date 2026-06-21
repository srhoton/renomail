package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/srhoton/renomail/internal/config"
	"github.com/srhoton/renomail/internal/store"
	"github.com/srhoton/renomail/internal/ui"
)

// runTUI opens the store and launches the Bubble Tea program. The cfg is accepted
// for symmetry with runDump and for the sync engine a later step adds; the TUI
// itself only needs the store today. The alternate screen is enabled on the root
// model's View (the v2 replacement for the WithAltScreen program option).
func runTUI(_ config.Config, paths config.Paths, opts ...tea.ProgramOption) error {
	m, st, err := buildTUI(paths)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if _, err := tea.NewProgram(m, opts...).Run(); err != nil {
		return fmt.Errorf("run tui: %w", err)
	}
	return nil
}

// buildTUI performs the testable wiring behind runTUI: ensure the data dir,
// open the store, and construct the root model. It is separated from the program
// launch (which needs a real terminal) so the setup path can be unit-tested. On
// any failure after the store opens, the store is closed before returning.
func buildTUI(paths config.Paths) (ui.Model, *store.Store, error) {
	// Mirror runDump: the data dir may not exist on a first run, and the SQLite
	// driver will not create it.
	if err := os.MkdirAll(paths.DataDir, 0o700); err != nil {
		return ui.Model{}, nil, fmt.Errorf("create data dir %s: %w", paths.DataDir, err)
	}
	st, err := store.Open(paths.DBFile)
	if err != nil {
		return ui.Model{}, nil, err
	}
	m, err := ui.New(st)
	if err != nil {
		_ = st.Close()
		return ui.Model{}, nil, err
	}
	return m, st, nil
}
