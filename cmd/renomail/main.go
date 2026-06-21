// Command renomail is the entrypoint for the renomail TUI. For now it resolves
// the application paths, loads the config, and prints a one-line summary so the
// scaffold is verifiably wired end to end. Later steps replace the body with the
// "dump" debug subcommand (step 03) and the TUI launch (step 04).
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/srhoton/renomail/internal/config"
)

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run resolves paths, loads the config, and writes the summary to w. It is kept
// separate from main so it can be exercised in tests without spawning a process.
func run(w io.Writer) error {
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	cfg, err := config.Load(paths.ConfigFile)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "renomail: %d gmail account(s), %d opml file(s), %d feed(s)\n",
		len(cfg.Gmail), len(cfg.OPML), len(cfg.Feed))
	return err
}
