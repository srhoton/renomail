# renomail

A single terminal inbox for everything you read: your RSS/Atom feeds **and** your
Gmail, side by side in one keyboard-driven TUI.

renomail fetches your feeds and (read-only) Gmail in the background, caches
everything locally in SQLite, and renders a unified, newest-first feed you can
filter, search, and read without leaving the terminal. Read/unread state is
**local** — marking something read never touches Gmail or a feed; it just dims the
row so you can tell what is new at a glance.

## Highlights

- **One feed for RSS + Gmail** — items from every source interleave, newest first.
- **Read-only Gmail** — the only OAuth scope requested is `gmail.readonly`; renomail
  never modifies a mailbox.
- **Background sync** — an initial sweep on launch, then periodic re-syncs; a spinner
  and a "synced N ago · M sources" indicator live in the status bar. One failing
  source surfaces its error on the status line without blocking the others.
- **Rich reading** — message and article bodies render as styled markdown (Glamour),
  re-wrapping when you resize the terminal.
- **Local-first** — everything is cached in SQLite, so the UI shows content instantly
  on launch, before any network sync completes.

## Install

```sh
go install github.com/srhoton/renomail/cmd/renomail@latest
```

Or from a clone:

```sh
git clone https://github.com/srhoton/renomail
cd renomail
go build ./cmd/renomail    # produces ./renomail
```

## Quick start

1. **Configure sources.** Create `~/.config/renomail/config.toml`:

   ```toml
   sync_interval = "5m"
   lookback      = "30d"

   [[opml]]
   path = "~/feeds.opml"

   [[gmail]]
   account = "me@gmail.com"
   ```

   See [docs/CONFIG.md](docs/CONFIG.md) for every option (multiple accounts, several
   OPML files, one-off feeds without OPML, and the duration format).

2. **Authorize Gmail (once per account).** Follow [docs/SETUP.md](docs/SETUP.md) to
   create a Google Cloud OAuth **Desktop** client and drop `credentials.json` at
   `~/.config/renomail/credentials.json`, then run:

   ```sh
   renomail auth me@gmail.com
   ```

   This opens the read-only consent screen in your browser and stores a refresh
   token; later runs are headless. RSS-only users can skip this step.

3. **Run it.**

   ```sh
   renomail
   ```

## Keybindings

| Key            | Action                                            |
| -------------- | ------------------------------------------------- |
| `j` / `↓`      | move down                                         |
| `k` / `↑`      | move up                                           |
| `g` / `G`      | jump to top / bottom                              |
| `Enter`        | open the selected item in the reader              |
| `Esc`          | back to the feed                                  |
| `o`            | open the current item's permalink in the browser  |
| `m`            | toggle the selected item's read flag              |
| `M`            | mark every item in the current filter read        |
| `/`            | search (substring over title, sender, and body)   |
| `e` / `r`      | show email only / RSS only                        |
| `u`            | cycle read filter: all → unread only → read only  |
| `a`            | reset all filters                                 |
| `R`            | sync now (force an immediate sweep)               |
| `?`            | toggle full help                                  |
| `q` / `Ctrl+C` | quit                                              |

## Subcommands

| Command                      | Purpose                                                   |
| ---------------------------- | -------------------------------------------------------- |
| `renomail`                   | launch the TUI (default)                                  |
| `renomail auth <account>`    | run the one-time Gmail consent flow for an account        |
| `renomail dump`              | print the cached/fetched feed to stdout (debug aid)       |

## Files & paths

renomail follows the XDG base-directory spec:

| Path                                              | Contents                            |
| ------------------------------------------------- | ----------------------------------- |
| `~/.config/renomail/config.toml`                  | configuration                       |
| `~/.config/renomail/credentials.json`             | Google OAuth Desktop client          |
| `~/.config/renomail/token-<account>.json`         | per-account OAuth token (mode 0600)  |
| `~/.local/share/renomail/renomail.db`             | SQLite cache (items, bodies, state)  |
| `~/.local/share/renomail/renomail.log`            | log file                            |

`XDG_CONFIG_HOME` / `XDG_DATA_HOME` are honored when set.

## Documentation

- [docs/SETUP.md](docs/SETUP.md) — Google Cloud OAuth setup and OPML export.
- [docs/CONFIG.md](docs/CONFIG.md) — the complete `config.toml` reference.

## Development

```sh
make build   # go build ./...
make test    # go test ./... -race -cover
make fmt     # gofmt -w
make vet     # go vet ./...
make cover   # coverage report
make run     # go run ./cmd/renomail
```
