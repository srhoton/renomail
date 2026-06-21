# 00 — Implementation Overview

This directory is the **build playbook** for renomail. It turns the architecture
in [`../DESIGN.md`](../DESIGN.md) into an ordered sequence of executable steps.
`DESIGN.md` remains the single source of truth for *what* we are building; these
documents describe *how* and *in what order*.

Read this overview first, then work the numbered docs in order. Each step is a
self-contained milestone that must be **build-green and test-green** before the
next begins.

---

## How to use these documents

- Every step doc follows the same 7-section template: **Goal · Prerequisites ·
  Deliverables · Design detail · Implementation flow · Validation criteria ·
  Done checklist.**
- Code blocks are **representative skeletons** — key types, signatures, and
  core-logic snippets that fix the conventions and shape. They are illustrative,
  not guaranteed-compiling; fill in bodies as you implement.
- Do not advance past a step until its **Done checklist** passes.
- If reality diverges from a skeleton (an API signature differs, a library
  changed), fix it in code *and* note the drift back in `DESIGN.md`.

---

## Step list

| # | Doc | Delivers |
|---|-----|----------|
| 1 | `01-scaffold-and-tooling.md` | Module, layout, config package, runnable stub |
| 2 | `02-model-and-store.md` | Domain types, SQLite store, `Provider` interface |
| 3 | `03-rss-and-opml.md` | RSS/OPML provider + `dump` debug command (no auth) |
| 4 | `04-tui-core.md` | Render pipeline + Bubble Tea feed list & reader |
| 5 | `05-filtering-and-read-state.md` | Filter bar, quick filters, read/unread persistence |
| 6 | `06-gmail-oauth.md` | Gmail provider, OAuth loopback, multi-account |
| 7 | `07-sync-engine.md` | Background concurrent + periodic sync, status line |
| 8 | `08-polish-and-docs.md` | Help, theming, resize, browser-open, README, coverage |

---

## Step dependency graph

```
01 scaffold ─┬─> 02 model+store ─┬─> 03 rss/opml ──┐
             │                   │                  ├─> 04 tui-core ─> 05 filter+read
             │                   └─> 06 gmail ──────┤                        │
             │                                      │                        v
             └──────────────────────────────────────┴─> 07 sync-engine ─> 08 polish
```

- **02** depends only on **01** (config + module exist).
- **03** and **06** both depend on **02** (they implement `Provider` and write to
  the store). They are independent of each other and could be built in parallel,
  but **03 comes first** deliberately (see ordering rationale).
- **04** depends on **02/03** (it renders cached items the providers produced).
- **05** depends on **04** (it extends the TUI).
- **07** depends on **02, 03, 06** (it orchestrates providers into the store) and
  on **04** (it pushes results into the UI).
- **08** is the final integration/polish pass over everything.

### Why RSS before Gmail

RSS needs **no authentication** and **no external setup**. Building it first lets
us exercise the entire data path — config → `Provider` → store → `Query` — and
even the TUI, against real content, before introducing the complexity of OAuth,
Google Cloud project setup, and MIME parsing. By the time we add Gmail (step 06),
the store, render pipeline, and UI are already proven; Gmail just becomes another
`Provider` implementation.

---

## Project-wide conventions

These apply across all steps. They mirror `DESIGN.md` and the Go best-practice
guide.

- **Go version:** latest stable (≥1.22). Pin in `go.mod`.
- **Module path:** `github.com/srhoton/renomail` (matches the GitHub remote).
- **Layout:** thin `cmd/renomail`; all logic in `internal/...` (see DESIGN.md §2).
  Organize by domain/feature, not by layer.
- **Errors:** return, don't panic, in normal flow. Wrap with context using
  `fmt.Errorf("doing X: %w", err)`. Define sentinel/typed errors where callers
  branch on them. Never swallow errors silently.
- **Context:** every network/DB call takes a `context.Context` with a timeout.
  Providers accept `ctx` as the first parameter.
- **Concurrency:** bound fan-out with a worker pool / semaphore; never spawn one
  goroutine per feed unboundedly. Use `errgroup` where collecting errors.
- **Logging:** a single leveled logger (stdlib `log/slog`), written to a file
  (not stdout — stdout belongs to the TUI). Default level INFO.
- **Formatting/vetting:** `gofmt` (or `gofumpt`) and `go vet` clean on every
  step. Optionally wire `golangci-lint`.
- **SQL:** always parameterized; never string-concatenate values into queries.
- **Naming/tests:** table-driven tests; fixtures in `testdata/`; temp DBs via
  `t.TempDir()`; `teatest` for TUI golden snapshots. Test names describe behavior.
- **Coverage target:** ≥80% on `model`, `store`, `source`, `syncengine`,
  `render`. UI coverage is best-effort via `Update()` + golden tests.

### Dependencies (added incrementally, per step)

| Module | Added in step | Purpose |
|--------|---------------|---------|
| `github.com/BurntSushi/toml` | 01 | Config |
| `modernc.org/sqlite` | 02 | Pure-Go SQLite |
| `github.com/mmcdole/gofeed` | 03 | RSS/Atom parsing |
| `github.com/gilliek/go-opml` | 03 | OPML import |
| `github.com/charmbracelet/bubbletea` | 04 | TUI runtime |
| `github.com/charmbracelet/bubbles` | 04 | List, viewport, textinput, spinner, help, key |
| `github.com/charmbracelet/lipgloss` | 04 | Styling |
| `github.com/charmbracelet/glamour` | 04 | Markdown rendering |
| `github.com/JohannesKaufmann/html-to-markdown/v2` | 04 | HTML → markdown |
| `golang.org/x/oauth2` (+ `/google`) | 06 | OAuth2 |
| `google.golang.org/api/gmail/v1` | 06 | Gmail API |
| `github.com/pkg/browser` | 08 | Open links |
| `golang.org/x/sync/errgroup` | 07 | Bounded concurrent fetch |
| `github.com/charmbracelet/x/exp/teatest` | 04 | TUI tests (test-only) |
| `github.com/zalando/go-keyring` *(optional)* | 06 | Token storage upgrade |

---

## Definition of done (whole project)

The build is complete when every step's checklist passes and the full
`DESIGN.md` §13 verification runs end-to-end: RSS and Gmail items interleave in a
single live feed, filters and read-state work and persist across restarts, and a
single failing source degrades gracefully without taking down the app.
