# 05 — Filtering & Read/Unread State

## Goal

Make the feed interactive: a `/` search bar, quick filter toggles
(`e`/`r`/`u`/`a`), optional source scoping, and read/unread management (`m`
toggle, `M` mark-all-read) that persists to the store and is reflected by row
styling. After this step the feed is fully navigable and read state survives
restarts and re-syncs.

## Prerequisites

- Step 04 (TUI core: app router, feed, reader).
- Uses `store.Query`, `store.SetRead`, `store.MarkAllRead` from step 02.

## Deliverables

```
internal/ui/filterbar/filterbar.go
internal/ui/filterbar/filterbar_test.go
internal/ui/app.go        # extended: filter state, read-toggle keys, re-query loop
internal/ui/messages.go   # readToggledMsg + setReadCmd / markAllReadCmd
internal/ui/keys/keys.go  # new bindings
internal/ui/app_test.go   # Update() tests for filter/read messages
```

## Design detail

### New key bindings (`keys/keys.go`)

```go
type KeyMap struct {
    // ... existing (Up, Down, Top, Bottom, Open, Back, Quit, Help) ...
    Search        key.Binding // "/"
    FilterEmail   key.Binding // "e"
    FilterRSS     key.Binding // "r"
    FilterUnread  key.Binding // "u"
    FilterAll     key.Binding // "a"
    ToggleRead    key.Binding // "m"
    MarkAllRead   key.Binding // "M"
}
```

### Filter bar (`filterbar/filterbar.go`)

A thin wrapper over `bubbles/textinput` shown only in `viewFilter`. It edits the
search term; Enter applies, Esc cancels.

```go
type Model struct{ input textinput.Model }

func New() Model { /* textinput with prompt "/ " */ }
func (m *Model) Focus() tea.Cmd { return m.input.Focus() }
func (m *Model) Value() string  { return m.input.Value() }
func (m *Model) SetValue(s string) { m.input.SetValue(s) }
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) { /* delegate to textinput */ }
func (m Model) View() string { return m.input.View() }
```

### Filter-changing commands (`messages.go`)

Every filter change rebuilds `m.filter` and re-runs `loadItemsCmd` (step 04) — a
single re-query path keeps the visible feed and the SQL in lockstep
(`DESIGN.md` §6.6).

```go
type readToggledMsg struct{ id string; read bool; err error }

func setReadCmd(st *store.Store, id string, read bool) tea.Cmd {
    return func() tea.Msg {
        err := st.SetRead(context.Background(), id, read)
        return readToggledMsg{id: id, read: read, err: err}
    }
}

func markAllReadCmd(st *store.Store, f model.Filter) tea.Cmd {
    return func() tea.Msg {
        if err := st.MarkAllRead(context.Background(), f); err != nil { return errMsg{err} }
        return itemsLoadedMsg{} // caller re-queries; or return reloadMsg to trigger loadItemsCmd
    }
}
```

### App wiring (`app.go`)

#### Quick filters and search

```go
func (m Model) handleFeedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    switch {
    case key.Matches(msg, m.keys.FilterEmail):
        m.filter.Kinds = map[model.Kind]bool{model.KindEmail: true}
        return m, loadItemsCmd(m.store, m.filter)
    case key.Matches(msg, m.keys.FilterRSS):
        m.filter.Kinds = map[model.Kind]bool{model.KindRSS: true}
        return m, loadItemsCmd(m.store, m.filter)
    case key.Matches(msg, m.keys.FilterUnread):
        m.filter.Read = model.ReadUnreadOnly
        return m, loadItemsCmd(m.store, m.filter)
    case key.Matches(msg, m.keys.FilterAll):
        m.filter = model.Filter{} // reset everything
        return m, loadItemsCmd(m.store, m.filter)
    case key.Matches(msg, m.keys.Search):
        m.view = viewFilter
        return m, m.filterbar.Focus()
    case key.Matches(msg, m.keys.ToggleRead):
        it, ok := m.feed.Selected(); if !ok { return m, nil }
        m.feed.SetReadLocal(it.ID, !it.Read)              // optimistic UI update
        return m, setReadCmd(m.store, it.ID, !it.Read)    // persist
    case key.Matches(msg, m.keys.MarkAllRead):
        return m, tea.Sequence(markAllReadCmd(m.store, m.filter), loadItemsCmd(m.store, m.filter))
    }
    return m.routeToChild(msg)
}
```

#### Applying search from the filter bar

```go
case viewFilter:
    switch {
    case key.Matches(msg, m.keys.Back): // esc cancels, keep old filter
        m.view = viewFeed
    case msg.Type == tea.KeyEnter:
        m.filter.Search = strings.TrimSpace(m.filterbar.Value())
        m.view = viewFeed
        return m, loadItemsCmd(m.store, m.filter)
    }
    var cmd tea.Cmd
    m.filterbar, cmd = m.filterbar.Update(msg)
    return m, cmd
```

#### Reflecting persisted read state

```go
case readToggledMsg:
    if msg.err != nil { m.status = msg.err.Error(); break }
    m.feed.SetReadLocal(msg.id, msg.read) // confirm optimistic update
```

`feed.SetReadLocal(id, read)` updates the in-memory row so the delegate restyles
it immediately (bright→faint) without a full re-query — unless the active filter
is `ReadUnreadOnly`, in which case re-query so the now-read item drops out.

### Status bar hint

Show the active filter compactly in the status bar, e.g.
`Filter: RSS · unread · "kubernetes"` so the current scope is always visible.

## Implementation flow

1. Add new key bindings.
2. Implement `filterbar` (+ test).
3. Add `setReadCmd`, `markAllReadCmd`, `readToggledMsg` to `messages.go`.
4. Extend `app.go`: filter-key handling, search apply/cancel, read toggle
   (optimistic + persist), mark-all-read (persist + re-query), status hint.
5. Add `feed.SetReadLocal` and filter-aware re-query behavior.
6. Tests + `gofmt`/`vet`/`build`/`test`.

## Validation criteria

- `go build ./...`, `go vet`, `gofmt` clean.
- **`Update()` tests:**
  - `e`/`r` set `filter.Kinds`; `u` sets `ReadUnreadOnly`; `a` resets; each emits
    a re-query command and the resulting `itemsLoadedMsg` updates the feed.
  - `/` enters `viewFilter`; typing + Enter sets `filter.Search` and re-queries;
    Esc cancels and leaves the filter unchanged.
  - `m` flips the selected row's read state (optimistic) and issues `setReadCmd`;
    `readToggledMsg` confirms it.
  - `M` issues mark-all-read then re-query; with `ReadUnreadOnly` active the feed
    empties.
- **Persistence test (integration with store):** mark items read, close, reopen
  the store, `Query` shows them read — and a simulated re-`UpsertItems` does
  **not** reset them (guard already in step 02, re-asserted here at the UI level).
- **Manual smoke:** toggle filters and search live; mark items read; quit and
  relaunch → read items remain dimmed.

## Done checklist

- [x] `/` search + `e`/`r`/`u`/`a` quick filters drive a single re-query path.
- [x] `m`/`M` update and persist read state; styling reflects it immediately.
- [x] Read state survives restart and re-sync.
- [x] Status bar shows the active filter.
- [x] `Update()` + persistence tests pass; `gofmt`/`vet`/`test` green.

## Implementation notes (deviations)

- **Filter-bar keys are dispatched before the global quit binding.** `handleKey`
  routes `viewFilter` to `handleFilterKey` first, so a literal `q` typed into the
  search box edits the term instead of quitting; only `ctrl+c` quits while
  editing. (The doc's snippet handled `viewFilter` inside the same switch as the
  `q` quit, which would have quit on every `q` keystroke.)
- **`e`/`r`/`u` compose onto the current filter; `a` is the full reset.** Setting
  a kind or unread scope leaves the search term intact, so `/term` then `u` shows
  unread matches. `a` resets `m.filter` to its zero value (clears everything).
- **`setReadCmd` returns `readToggledMsg`** (not `nil`), so the optimistic
  in-memory flip is confirmed by id. Under an unread-only filter the
  `readToggledMsg` handler re-queries so the now-read item drops out; otherwise it
  confirms the row in place via `feed.SetReadLocal`.
- **`markAllReadCmd` returns `reloadMsg`** rather than chaining a re-query with
  `tea.Sequence`, so the re-query is guaranteed to run after the write.
- **No new module dependency:** `bubbles/v2/textinput` ships inside the already
  direct `bubbles/v2` module, so `go.mod` is unchanged.
- **Source scoping (`SourceIDs`) deferred:** the store supports it, but no key
  binding/UI is assigned in this step.
