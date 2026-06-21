// Command renomail is the entrypoint for the renomail TUI. It resolves the
// application paths, loads the config, and either runs the "dump" debug
// subcommand (step 03) or launches the Bubble Tea TUI by default (step 04).
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/srhoton/renomail/internal/config"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

// run dispatches the given args and returns a process exit code. It is split from
// main so the dispatch + error-reporting wiring is testable without os.Exit.
func run(args []string, w io.Writer) int {
	if err := dispatch(context.Background(), args, w); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// dispatch resolves paths and config once, then routes to a subcommand. It is
// kept separate from main so the wiring can be exercised in tests without
// spawning a process. The "dump" subcommand runs the debug pipeline to the
// writer; anything else (including no arguments) launches the TUI.
func dispatch(ctx context.Context, args []string, w io.Writer) error {
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	cfg, err := config.Load(paths.ConfigFile)
	if err != nil {
		return err
	}

	if len(args) > 0 {
		switch args[0] {
		case "dump":
			return runDump(ctx, cfg, paths, w)
		case "auth":
			return runAuth(ctx, paths, authAccount(args[1:]))
		}
	}
	return runTUI(cfg, paths)
}
