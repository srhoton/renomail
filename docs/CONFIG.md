# Configuration — `config.toml`

renomail reads a single TOML file at `~/.config/renomail/config.toml`
(`$XDG_CONFIG_HOME/renomail/config.toml` when that variable is set). The file is
optional: with none present renomail runs with defaults and no sources. Missing
individual keys fall back to their defaults.

## Top-level keys

| Key                  | Type   | Default | Description                                                        |
| -------------------- | ------ | ------- | ------------------------------------------------------------------ |
| `sync_interval`      | string | `"5m"`  | How often the background engine re-syncs every source.             |
| `lookback`           | string | `"30d"` | How far back the **first** Gmail sweep looks (the cold-start window). |
| `tmux_notifications` | bool   | `true`  | When running inside tmux, post a status-line message as new items arrive. Set `false` to disable. Has no effect outside tmux. |

After the first sync each source advances its own `LastSync`, so steady-state
syncs only fetch what is new; `lookback` bounds only that initial scan.

### Duration format

Durations accept standard Go syntax (`s`, `m`, `h`) **plus** a `d` (days) suffix
that Go does not natively support:

| Example  | Meaning      |
| -------- | ------------ |
| `"90s"`  | 90 seconds   |
| `"10m"`  | 10 minutes   |
| `"2h"`   | 2 hours      |
| `"7d"`   | 7 days       |
| `"1.5d"` | 36 hours     |

An empty or omitted value uses the default above.

## Gmail accounts — `[[gmail]]`

One block per Gmail account. The account email is also the source identifier and
display name. Each account must be authorized once with `renomail auth <account>`
(see [SETUP.md](SETUP.md)).

| Key       | Type   | Required | Description              |
| --------- | ------ | -------- | ------------------------ |
| `account` | string | yes      | The Gmail address to read. |

```toml
[[gmail]]
account = "me@gmail.com"

[[gmail]]
account = "work@gmail.com"
```

Omit all `[[gmail]]` blocks to run RSS-only (no `credentials.json` needed).

## Apple Mail accounts — `[apple_mail]`

macOS only. A single flag opts in the local, read-only Apple Mail source; when on,
**every** account Apple Mail has on disk is discovered automatically and each account's
**Inbox** is folded into the feed. There is no per-account setup — renomail reads a
private copy of Apple Mail's local index and never writes to `~/Library/Mail`.

| Key       | Type | Required | Description                                  |
| --------- | ---- | -------- | -------------------------------------------- |
| `enabled` | bool | no       | `true` turns the source on. Default: `false`. |

```toml
[apple_mail]
enabled = true
```

Reading Apple Mail's data requires **Full Disk Access** for the terminal running
renomail (System Settings → Privacy & Security → Full Disk Access). Without it renomail
keeps running — your feeds and Gmail are unaffected — and surfaces a one-line reminder
on the status bar instead of failing.

Notes:

- **Inbox only.** Sent, Junk, Archive, and custom mailboxes are not ingested.
- **Read-only.** Reading an item in renomail never changes Apple Mail; new arrivals
  trigger the same notifications as any other source.
- Bodies load on demand from the local `.emlx` files; a not-yet-downloaded message
  falls back to its snippet. Each item also carries a `message://` link that opens it
  in Mail.app.
- Gmail accounts in Apple Mail usually keep an empty local Inbox (mail lives under
  `[Gmail]/All Mail`), so this primarily surfaces iCloud/Exchange/IMAP inboxes; use the
  native `[[gmail]]` integration for Gmail.
- Off macOS the flag is accepted but yields no providers (a single advisory warning).

## Feeds via OPML — `[[opml]]`

One block per OPML file. Every feed in the file becomes a source. `~` is expanded
to your home directory.

| Key    | Type   | Required | Description                  |
| ------ | ------ | -------- | ---------------------------- |
| `path` | string | yes      | Path to an OPML/XML feed list. |

```toml
[[opml]]
path = "~/feeds.opml"

[[opml]]
path = "~/work-feeds.opml"
```

## One-off feeds — `[[feed]]`

For a handful of feeds you do not want to manage in an OPML file.

| Key     | Type   | Required | Description                          |
| ------- | ------ | -------- | ------------------------------------ |
| `url`   | string | yes      | RSS/Atom feed URL.                   |
| `title` | string | no       | Display name (falls back to the feed's own title). |

```toml
[[feed]]
url   = "https://example.com/blog/feed.xml"
title = "Example Blog"
```

## Slack notifications — `[slack]`

An optional Slack [incoming webhook](https://api.slack.com/messaging/webhooks) digest.
When configured, renomail posts **one** message per sync sweep that finds new items,
grouped by source with linked titles (and the sender for emails) and capped with a
"…and N more" line. The initial sweep on launch is never posted. Independent of tmux —
both channels may be on at once.

| Key           | Type   | Required | Description                                                                 |
| ------------- | ------ | -------- | --------------------------------------------------------------------------- |
| `webhook_url` | string | no\*     | Slack incoming-webhook URL (must be `https://`). May be supplied via env instead. |
| `max_items`   | int    | no       | Item lines per digest before the remainder collapses to "…and N more". Default `10`. |

\* The webhook may instead be provided by the `RENOMAIL_SLACK_WEBHOOK` environment
variable, which **takes precedence** over `webhook_url` — handy for keeping the secret
out of the config file. Slack is disabled when neither the env var nor `webhook_url`
is set. A non-`https` `webhook_url` is rejected at startup.

```toml
[slack]
webhook_url = "https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXX"
max_items   = 10
```

## Complete example

```toml
sync_interval      = "10m"
lookback           = "14d"
tmux_notifications = true

[[gmail]]
account = "me@gmail.com"

[[gmail]]
account = "work@gmail.com"

[[opml]]
path = "~/feeds.opml"

[[feed]]
url   = "https://example.com/blog/feed.xml"
title = "Example Blog"

[slack]
webhook_url = "https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXX"
```

## File locations

renomail follows the XDG base-directory spec (`XDG_CONFIG_HOME` defaults to
`~/.config`, `XDG_DATA_HOME` to `~/.local/share`):

| Path                                       | Contents                              |
| ------------------------------------------ | ------------------------------------- |
| `<config>/renomail/config.toml`            | this file                             |
| `<config>/renomail/credentials.json`       | Google OAuth Desktop client            |
| `<config>/renomail/token-<account>.json`   | per-account OAuth token (mode `0600`)  |
| `<data>/renomail/renomail.db`              | SQLite cache (items, bodies, read state) |
| `<data>/renomail/renomail.log`             | log file                              |
