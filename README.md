# renomail

A single terminal inbox for everything you read: your RSS/Atom feeds, your Gmail,
**and** (on macOS) your local Apple Mail accounts, side by side in one keyboard-driven
TUI.

renomail fetches your feeds and Gmail in the background, caches everything locally in
SQLite, and renders a unified, newest-first feed you can filter, search, and read
without leaving the terminal. Read/unread state is tracked locally and **synced back to
the source**: marking a message read (or unread) in renomail also marks it read in Gmail
and in Apple Mail / Mail.app, so your accounts stay in step. Feeds, which have no such
notion, just dim the row.

## Highlights

- **One feed for RSS + Gmail + Apple Mail** — items from every source interleave,
  newest first.
- **Read-state sync, nothing more** — the only OAuth scope requested is `gmail.modify`,
  used solely to toggle the `UNREAD` label when you mark a message read/unread; renomail
  never deletes, moves, or sends mail.
- **Apple Mail (macOS)** — opt in with a single flag to surface every Apple Mail
  account's inbox straight off disk, no extra credentials. renomail reads a copy of Apple
  Mail's local index (never writing to `~/Library/Mail`); marking a message read drives
  Mail.app via AppleScript so the change — and, for accounts like a work Gmail, the
  server — stays in sync.
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

   This opens the consent screen (the `gmail.modify` scope — read mail and toggle the
   read/unread flag) in your browser and stores a refresh token; later runs are headless.
   RSS-only users can skip this step. **Upgrading from an older read-only install:** re-run
   `renomail auth <account>` once per account to grant the new scope; until you do,
   marking read still works locally but the write-back to Gmail prompts you to re-auth.

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
| `S`            | mark every item from the selected source read     |
| `/`            | search (substring over title, sender, and body)   |
| `e` / `r`      | show email only / RSS only                        |
| `u`            | cycle read filter: all → unread only → read only  |
| `a`            | reset all filters                                 |
| `R`            | sync now (force an immediate sweep)               |
| `?`            | toggle full help                                  |
| `q` / `Ctrl+C` | quit                                              |

## Apple Mail (macOS)

On macOS, renomail can fold your Apple Mail (Mail.app) accounts into the same feed.
It reads a private copy of Apple Mail's local index — no IMAP setup, no credentials —
and lists each account's **Inbox**. Bodies are loaded on demand from the local message
files, and each item carries a `message://` link that opens it in Mail.app. renomail
never writes to `~/Library/Mail`; marking a message read instead drives Mail.app via
AppleScript (`osascript`), which sets the message's read status — launching Mail.app if
it isn't already running, and for an account such as a work Gmail, propagating to the
server on Mail's next sync.

Enable it with a single flag (all discovered accounts are included):

```toml
[apple_mail]
enabled = true
```

Reading Apple Mail's data requires **Full Disk Access** for your terminal: grant it in
System Settings → Privacy & Security → Full Disk Access. Without it, renomail keeps
running (your feeds and Gmail are unaffected) and shows a one-line reminder on the
status bar. New Apple Mail arrivals trigger the same notifications as every other
source. See [docs/CONFIG.md](docs/CONFIG.md#apple-mail-accounts--apple_mail) for details.

Gmail accounts added to Apple Mail usually keep an empty local Inbox (mail lives under
`[Gmail]/All Mail`), so Apple Mail naturally surfaces your iCloud/Exchange/IMAP inboxes
while the native **Read-only Gmail** integration handles Gmail.

## Notifications

renomail offers two independent "new items arrived" channels. Both skip the initial
sweep on launch (so a first run does not announce its whole backfill) and both are
best-effort — a delivery failure shows on the status line, never crashing the app.

### tmux

When renomail runs inside a **tmux** session, it posts a brief message to the tmux
status line each time a background sync pulls in new items — e.g.
`renomail: 3 new from Hacker News` — so you get a heads-up without switching back to
its window. One message is sent per source that gained items. Outside tmux nothing
is emitted.

This is on by default whenever `$TMUX` is set. To turn it off, add to your
`config.toml`:

```toml
tmux_notifications = false
```

### Slack

Point renomail at a Slack [incoming webhook](https://api.slack.com/messaging/webhooks)
and it posts a single, richly formatted digest per sync sweep that finds new items —
grouped by source, with linked titles (and the sender for emails), capped with a
"…and N more" line. Coalescing into one message per sweep keeps it well under Slack's
webhook rate limit.

```toml
[slack]
webhook_url = "https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXX"
max_items   = 10   # optional; item lines per digest before "…and N more"
```

To keep the secret out of the config file, set `RENOMAIL_SLACK_WEBHOOK` instead — it
takes precedence over `webhook_url`. Slack is disabled when neither is set. See
[docs/CONFIG.md](docs/CONFIG.md#slack-notifications--slack) for details.

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
