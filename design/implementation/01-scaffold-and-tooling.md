# 01 — Scaffold & Tooling

## Goal

Stand up the Go module, the package layout from `DESIGN.md` §2, a runnable (but
empty) `cmd/renomail` entrypoint, and the `internal/config` package that loads
and saves the TOML config and resolves XDG paths. After this step the project
compiles and can read its own configuration. This is the foundation every later
step builds on.

## Prerequisites

- None (first step). A Go toolchain ≥1.22 installed.

## Deliverables

```
go.mod
cmd/renomail/main.go
internal/config/config.go
internal/config/config_test.go
internal/config/paths.go
.editorconfig            (optional)
Makefile                 (optional)
```

## Design detail

### Module init & dependencies

```bash
go mod init github.com/srhoton/renomail
go get github.com/BurntSushi/toml@latest
```

### Config types (`internal/config/config.go`)

The config mirrors `DESIGN.md` §8. Durations are stored as strings and parsed on
load so the TOML stays human-friendly.

```go
package config

type Config struct {
    SyncInterval string        `toml:"sync_interval"` // e.g. "5m"
    Lookback     string        `toml:"lookback"`      // e.g. "30d" (Gmail window)
    Gmail        []GmailAccount `toml:"gmail"`
    OPML         []OPMLSource   `toml:"opml"`
    Feed         []FeedSource   `toml:"feed"` // one-off feeds without OPML
}

type GmailAccount struct {
    Account string `toml:"account"` // email address; also the SourceID
}

type OPMLSource struct {
    Path string `toml:"path"`
}

type FeedSource struct {
    URL   string `toml:"url"`
    Title string `toml:"title"`
}

// Parsed convenience accessors (computed, not serialized).
func (c Config) SyncEvery() (time.Duration, error) { /* parse SyncInterval, default 5m */ }
func (c Config) LookbackDuration() (time.Duration, error) { /* parse "30d"/"720h", default 30d */ }
```

> Note: Go's `time.ParseDuration` does **not** understand `"d"`. Implement a small
> helper that accepts a `Nd` suffix and converts to hours, falling back to
> `time.ParseDuration` for everything else.

### Load / Save

```go
// Load reads the config file; if it does not exist, returns a Config with
// sensible defaults and no error (first run is not an error).
func Load(path string) (Config, error) {
    var c Config
    b, err := os.ReadFile(path)
    if errors.Is(err, os.ErrNotExist) {
        return Default(), nil
    }
    if err != nil {
        return c, fmt.Errorf("read config %s: %w", path, err)
    }
    if err := toml.Unmarshal(b, &c); err != nil {
        return c, fmt.Errorf("parse config %s: %w", path, err)
    }
    return c.withDefaults(), nil
}

func Save(path string, c Config) error { /* toml.Marshal, MkdirAll, WriteFile 0600 */ }

func Default() Config {
    return Config{SyncInterval: "5m", Lookback: "30d"}
}
```

### Paths (`internal/config/paths.go`)

Resolve XDG dirs once and expand `~`. Config and tokens live in the config dir;
the DB lives in the data dir (DESIGN.md §5, §8).

```go
type Paths struct {
    ConfigDir   string // ~/.config/renomail
    DataDir     string // ~/.local/share/renomail
    ConfigFile  string // <ConfigDir>/config.toml
    DBFile      string // <DataDir>/renomail.db
    Credentials string // <ConfigDir>/credentials.json (Gmail OAuth client)
    LogFile     string // <DataDir>/renomail.log
}

// ResolvePaths honors XDG_CONFIG_HOME / XDG_DATA_HOME, else falls back to the
// platform defaults via os.UserConfigDir / os.UserHomeDir.
func ResolvePaths() (Paths, error)

// ExpandTilde turns a leading "~" into the user's home directory.
func ExpandTilde(p string) (string, error)

// TokenFile returns <ConfigDir>/token-<account>.json for a Gmail account.
func (p Paths) TokenFile(account string) string
```

### Entrypoint stub (`cmd/renomail/main.go`)

Keep `main` thin: resolve paths, load config, and (for now) print a summary so we
can see it works. Later steps replace the body with the `dump` subcommand (03)
and the TUI launch (04).

```go
func main() {
    paths, err := config.ResolvePaths()
    if err != nil { fatal(err) }
    cfg, err := config.Load(paths.ConfigFile)
    if err != nil { fatal(err) }
    fmt.Printf("renomail: %d gmail account(s), %d opml file(s), %d feed(s)\n",
        len(cfg.Gmail), len(cfg.OPML), len(cfg.Feed))
}

func fatal(err error) { fmt.Fprintln(os.Stderr, "error:", err); os.Exit(1) }
```

### Optional tooling

- `.editorconfig`: tabs for Go, final newline, UTF-8.
- `Makefile`: `build`, `test`, `fmt` (`gofmt -l -w .`), `vet`, `lint` targets.

## Implementation flow

1. `go mod init`; add `BurntSushi/toml`.
2. Create the directory tree from DESIGN.md §2 (empty packages are fine for now).
3. Implement `paths.go` (`ResolvePaths`, `ExpandTilde`, `TokenFile`).
4. Implement `config.go` (`Config`, `Default`, `withDefaults`, `Load`, `Save`,
   duration helpers).
5. Implement the `main.go` stub.
6. Write `config_test.go` (round-trip + defaults + duration parsing).
7. `gofmt`, `go vet`, `go build ./...`, `go test ./...`.

## Validation criteria

- `go build ./...` succeeds; `go vet ./...` and `gofmt -l .` are clean.
- **Unit tests (`config_test.go`):**
  - `Load` of a missing file returns `Default()` and no error.
  - Round-trip: `Save` then `Load` yields an equal `Config`.
  - `SyncEvery()`/`LookbackDuration()` parse `"5m"`/`"30d"` correctly and apply
    defaults on empty input; malformed values return a wrapped error.
  - `ExpandTilde("~/x")` resolves to the home dir; non-tilde paths pass through.
- **Manual smoke:** `go run ./cmd/renomail` prints the zero-counts summary on a
  machine with no config file (first-run path).

## Done checklist

- [ ] Module initialized, layout created, builds clean.
- [ ] `config.Load/Save` + path resolution implemented and unit-tested.
- [ ] `cmd/renomail` runs and reports config counts.
- [ ] `gofmt`/`go vet`/`go test` all green.
