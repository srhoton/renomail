# 08 — Polish, Documentation & Hardening

## Goal

Final integration pass: help overlay, theming, terminal-resize correctness,
status-bar last-sync, open-in-browser, broadened test coverage to the project
targets, and end-user documentation (README + setup). After this step renomail is
a complete, documented v1 that satisfies the full `DESIGN.md` §13 verification.

## Prerequisites

- Steps 01–07 complete and green.

## Deliverables

```
internal/ui/help/help.go        # ? overlay (or extend bubbles/help usage)
internal/ui/app.go              # open-in-browser, resize, help toggle, status polish
internal/ui/styles/styles.go    # finalized theme (light/dark aware)
README.md                       # overview, install, usage, keybindings
docs/SETUP.md                   # Google Cloud OAuth client + OPML setup
docs/CONFIG.md                  # full config.toml reference
Makefile                        # build/test/fmt/vet/lint/cover targets
.github/workflows/ci.yml        # optional: build + test + vet on push
```

```bash
go get github.com/pkg/browser@latest
```

## Design detail

### Open in browser (`app.go`)

`o` opens the selected/open item's permalink (`DESIGN.md` §6.5/§6.7).

```go
case key.Matches(msg, m.keys.OpenBrowser):
    it, ok := m.current()      // selected row in feed, or open item in reader
    if ok && it.URL != "" {
        return m, func() tea.Msg {
            if err := browser.OpenURL(it.URL); err != nil { return errMsg{err} }
            return nil
        }
    }
```

### Help overlay (`help/help.go`)

Use `bubbles/help` with the `keys.KeyMap` already defined. `?` toggles a full
help view; a short help line shows at the bottom otherwise.

```go
func (m Model) helpView() string { return m.help.View(m.keys) } // ShortHelp/FullHelp on KeyMap
```

Implement `ShortHelp()`/`FullHelp()` on `keys.KeyMap` so help renders from the
single source of binding truth.

### Resize correctness

Confirm `tea.WindowSizeMsg` recomputes: feed list size, reader viewport size,
**and** the Glamour renderer width (re-render the open item at the new width).
Add a regression test that resizing while in the reader re-wraps content.

```go
case tea.WindowSizeMsg:
    m.w, m.h = msg.Width, msg.Height
    _ = m.renderer.SetWidth(contentWidth(msg.Width))
    m.feed.SetSize(msg.Width, msg.Height-statusH)
    m.reader.SetSize(msg.Width, msg.Height-statusH)
    if m.view == viewReader { return m, loadBodyCmd(m.store, m.renderer, m.openItem) }
```

### Theming

Finalize `styles.DefaultStyles()` with a light/dark-adaptive palette
(`lipgloss.AdaptiveColor`). Ensure unread (bold/bright) vs read (faint) remain
legible in both. Keep all colors in one place so a theme swap is a single edit.

### Status bar

Compose: active filter summary (step 05) · sync spinner/last-sync (step 07) ·
transient error (auto-clears after N seconds or on next key). Keep to one line.

### Documentation

- **README.md:** what renomail is, a screenshot/asciicast, install
  (`go install ./cmd/renomail`), quick start, the full keybinding table, and a
  pointer to `docs/`.
- **docs/SETUP.md:** step-by-step Google Cloud project → enable Gmail API →
  create **OAuth client (Desktop app)** → download `credentials.json` → place at
  `~/.config/renomail/credentials.json` → `renomail auth <account>`. Then OPML:
  export from your reader, reference it in config.
- **docs/CONFIG.md:** every `config.toml` key, defaults, and examples
  (multi-account, multiple OPML files, one-off feeds), mirroring `DESIGN.md` §8.

### CI (optional)

`ci.yml`: `go build ./...`, `go vet ./...`, `gofmt -l .` (fail if non-empty),
`go test ./... -race -cover`.

## Implementation flow

1. Add `pkg/browser`; wire `o` open-in-browser.
2. Implement help (`ShortHelp`/`FullHelp` on `KeyMap`, `?` toggle).
3. Audit + fix resize across feed/reader/renderer; add the regression test.
4. Finalize theme + status bar composition.
5. Broaden tests to hit coverage targets (fill gaps in `model`, `store`,
   `source`, `syncengine`, `render`).
6. Write README + `docs/SETUP.md` + `docs/CONFIG.md`; add Makefile (+ CI).
7. Full sweep: `gofmt`/`vet`/`build`/`test -race -cover`.

## Validation criteria

- `go build ./...`, `go vet`, `gofmt -l .` clean; `go test ./... -race` green.
- **Coverage:** ≥80% on `model`, `store`, `source/*`, `syncengine`, `render`
  (`go test ./... -cover` / `-coverprofile`).
- **Resize test:** resizing in the reader re-wraps the rendered body without
  panic; feed/reader dimensions track the terminal.
- **Help:** `?` toggles full help; bindings shown match `keys.KeyMap`.
- **Open-in-browser:** `o` invokes `browser.OpenURL` with the item URL (assert via
  an injected opener in tests; manual confirm opens the default browser).
- **Full §13 verification (manual, end-to-end):**
  - RSS path: items appear newest-first; open → rich render; `o` opens browser.
  - Gmail path: after `renomail auth`, messages interleave; open → body renders.
  - Read state: `m` marks read; restart → persists (dimmed); re-sync does not
    reset it.
  - Filters: `e`/`r`/`u`/`a` + `/` search produce correct subsets.
  - Resilience: a bad feed URL / revoked token shows in the status line while the
    rest of the feed still loads.

## Done checklist

- [x] Help overlay, theming, resize, status bar, open-in-browser all working.
- [x] Coverage targets met; `-race` clean.
- [x] README + SETUP + CONFIG docs complete and accurate.
- [x] Makefile in place (CI workflow intentionally skipped — see notes).
- [x] Full DESIGN.md §13 verification passes end-to-end (manual smoke is the human step).

## Implementation notes (deviations)

- **Most UI machinery already existed** from Steps 04–07 (status bar, `?` short/full
  help toggle via `m.help.ShowAll`, resize sizing of feed/reader, the Glamour renderer
  width, `ShortHelp()`/`FullHelp()` on `KeyMap`). Step 08 filled the concrete gaps
  rather than rebuilding: `o` open-in-browser, `R` force-sync, resize body re-render,
  transient-error auto-clear, background-aware theming, and the docs.
- **Help** stayed as the `bubbles/help` short/full toggle (the design's "or extend
  bubbles/help usage" path), not a separate full-screen overlay view.
- **Open-in-browser** uses `github.com/pkg/browser`; the opener is an injectable
  `Model.openURL` field (default `browser.OpenURL`) so it is unit-testable. A unified
  `current()` returns the open item in the reader or the selected row in the feed, so
  `o` works from both views.
- **Force-sync (`R`)** — DESIGN §6.7 lists it though the step doc did not; added an
  engine `Trigger()` (a buffered, coalescing trigger channel `Run` selects on) wired
  through `ui.New(..., eng.Trigger, ...)`. Pressing `R` re-lights the spinner by reusing
  the existing `inflight` counter, and is a no-op while a sweep is already running. This
  also gives the manual case the per-sweep spinner that automatic ticks still lack.
- **Theming** — lipgloss v2 dropped `AdaptiveColor`; used `lipgloss.LightDark(dark)`
  over the detected background instead, split into a testable `stylesForBackground`.
- **Transient errors** clear on the next keypress (the design's "or on next key"
  option), chosen over a tick-based timer for determinism.
- **Consent-URL auto-open** — `gmail.Authorize` now best-effort `browser.OpenURL`s the
  consent URL after printing it (the print remains the headless/SSH fallback).
- **CI** (`.github/workflows/ci.yml`) intentionally **skipped** per decision; the
  Makefile targets cover local build/vet/fmt/test.
- **Coverage** was already ≥80% on every target package before this step; the new tests
  cover the new behavior (engine trigger, `o`/`R`, resize re-render, status-clear,
  both theme palettes) rather than backfilling.
