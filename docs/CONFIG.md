# Configuration — `config.toml`

renomail reads a single TOML file at `~/.config/renomail/config.toml`
(`$XDG_CONFIG_HOME/renomail/config.toml` when that variable is set). The file is
optional: with none present renomail runs with defaults and no sources. Missing
individual keys fall back to their defaults.

## Top-level keys

| Key             | Type   | Default | Description                                                        |
| --------------- | ------ | ------- | ------------------------------------------------------------------ |
| `sync_interval` | string | `"5m"`  | How often the background engine re-syncs every source.             |
| `lookback`      | string | `"30d"` | How far back the **first** Gmail sweep looks (the cold-start window). |

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

## Complete example

```toml
sync_interval = "10m"
lookback      = "14d"

[[gmail]]
account = "me@gmail.com"

[[gmail]]
account = "work@gmail.com"

[[opml]]
path = "~/feeds.opml"

[[feed]]
url   = "https://example.com/blog/feed.xml"
title = "Example Blog"
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
