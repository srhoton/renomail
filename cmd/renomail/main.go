// Command renomail is the entrypoint for the renomail TUI. For now it resolves
// the application paths, loads the config, and either runs the "dump" debug
// subcommand (step 03) or prints a one-line summary so the scaffold is verifiably
// wired end to end. A later step replaces the default body with the TUI launch
// (step 04).
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/srhoton/renomail/internal/config"
)

func main() {
	if err := dispatch(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// dispatch resolves paths and config once, then routes to a subcommand. It is
// kept separate from main so it can be exercised in tests without spawning a
// process. The only subcommand today is "dump"; anything else (including no
// arguments) prints the config summary.
func dispatch(ctx context.Context, args []string, w io.Writer) error {
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	cfg, err := config.Load(paths.ConfigFile)
	if err != nil {
		return err
	}

	if len(args) > 0 && args[0] == "dump" {
		return runDump(ctx, cfg, paths, w)
	}
	return summarize(w, cfg)
}

// summarize writes the one-line config summary to w.
func summarize(w io.Writer, cfg config.Config) error {
	_, err := fmt.Fprintf(w, "renomail: %d gmail account(s), %d opml file(s), %d feed(s)\n",
		len(cfg.Gmail), len(cfg.OPML), len(cfg.Feed))
	return err
}
