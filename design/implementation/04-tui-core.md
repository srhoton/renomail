# 04 — TUI Core (Render, Feed List, Reader)

## Goal

Build the Bubble Tea application shell over the cached store: a full-screen feed
list that drills into a full-screen reader, plus the HTML→markdown→Glamour render
pipeline. After this step `renomail` launches as a TUI showing the RSS items from
step 03, and Enter opens an item into a styled, scrollable reader.

## Prerequisites

- Step 02 (store) and step 03 (RSS items in the DB to display).

## Deliverables

```
internal/render/render.go
internal/render/render_test.go
internal/ui/app.go            # root Model (view router)
internal/ui/messages.go       # tea.Msg types + commands
internal/ui/keys/keys.go      # key bindings
internal/ui/styles/styles.go  # lipgloss theme
internal/ui/feed/feed.go      # list model + item delegate
internal/ui/reader/reader.go  # viewport-based reader
internal/ui/app_test.go       # Update() + teatest snapshots
cmd/renomail/main.go          # launch the TUI by default
```

```bash
go get github.com/charmbracelet/bubbletea@latest
go get github.com/charmbracelet/bubbles@latest
go get github.com/charmbracelet/lipgloss@latest
go get github.com/charmbracelet/glamour@latest
go get github.com/JohannesKaufmann/html-to-markdown/v2@latest
go get github.com/charmbracelet/x/exp/teatest@latest   # test-only
```

## Design detail

### Render pipeline (`internal/render/render.go`)

`DESIGN.md` §7. Width-aware; falls back to plain text when there is no HTML.

```go
type Renderer struct {
    width int
    md    *glamour.TermRenderer // rebuilt on width change
}

func New(width int) (*Renderer, error) { /* glamour.NewTermRenderer(AutoStyle, WordWrap(width)) */ }

func (r *Renderer) SetWidth(w int) error // rebuild glamour renderer at new width

// Render converts an item's body to terminal-ready styled text.
func (r *Renderer) Render(it model.Item) (string, error) {
    if strings.TrimSpace(it.BodyHTML) == "" {
        return r.md.Render(it.BodyText) // already plain; glamour still wraps
    }
    md, err := htmltomarkdown.ConvertString(it.BodyHTML) // strips script/style
    if err != nil { return "", fmt.Errorf("html->md: %w", err) }
    return r.md.Render(md)
}
```

### Messages & commands (`internal/ui/messages.go`)

```go
type itemsLoadedMsg struct{ items []model.Item }
type bodyLoadedMsg  struct{ id, rendered string; err error }
type errMsg         struct{ err error }

// loadItemsCmd queries the store off the UI goroutine.
func loadItemsCmd(st *store.Store, f model.Filter) tea.Cmd {
    return func() tea.Msg {
        items, err := st.Query(context.Background(), f)
        if err != nil { return errMsg{err} }
        return itemsLoadedMsg{items}
    }
}

// loadBodyCmd fetches (lazily) + renders an item's body.
func loadBodyCmd(st *store.Store, r *render.Renderer, it model.Item) tea.Cmd {
    return func() tea.Msg {
        // For RSS the body is already present; the lazy-provider path arrives in 06/07.
        out, err := r.Render(it)
        return bodyLoadedMsg{id: it.ID, rendered: out, err: err}
    }
}
```

### Root model (`internal/ui/app.go`)

`DESIGN.md` §6.1. Routes between views and owns the store + renderer.

```go
type view int
const (viewFeed view = iota; viewReader; viewFilter; viewHelp)

type Model struct {
    view     view
    feed     feed.Model
    reader   reader.Model
    filter   model.Filter
    store    *store.Store
    renderer *render.Renderer
    keys     keys.KeyMap
    help     help.Model
    status   string
    w, h     int
}

func New(st *store.Store) (Model, error) { /* init feed, reader, renderer, keys */ }

func (m Model) Init() tea.Cmd { return loadItemsCmd(m.store, m.filter) }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.WindowSizeMsg:
        m.w, m.h = msg.Width, msg.Height
        _ = m.renderer.SetWidth(contentWidth(msg.Width))
        m.feed.SetSize(msg.Width, msg.Height-1)
        m.reader.SetSize(msg.Width, msg.Height-1)
    case itemsLoadedMsg:
        m.feed.SetItems(msg.items)
    case bodyLoadedMsg:
        if msg.err != nil { m.status = msg.err.Error(); break }
        m.reader.SetContent(msg.rendered)
    case tea.KeyMsg:
        return m.handleKey(msg)
    }
    return m.routeToChild(msg) // delegate to feed/reader based on m.view
}

func (m Model) View() string {
    switch m.view {
    case viewReader: return m.reader.View()
    default:         return m.feed.View() + "\n" + m.statusBar()
    }
}
```

#### Key handling (drill-in / back / open)

```go
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    switch m.view {
    case viewFeed:
        switch {
        case key.Matches(msg, m.keys.Open):
            it, ok := m.feed.Selected(); if !ok { return m, nil }
            m.view = viewReader
            cmds := []tea.Cmd{loadBodyCmd(m.store, m.renderer, it)}
            if !it.Read { cmds = append(cmds, setReadCmd(m.store, it.ID, true)) } // 05 wires styling
            return m, tea.Batch(cmds...)
        case key.Matches(msg, m.keys.Quit):
            return m, tea.Quit
        }
    case viewReader:
        if key.Matches(msg, m.keys.Back) { m.view = viewFeed; return m, nil }
    }
    // pass through to the active child (list/viewport scrolling)
    return m.routeToChild(msg)
}
```

### Feed list + delegate (`internal/ui/feed/feed.go`)

Wrap `bubbles/list` with a custom delegate that renders one row per
`DESIGN.md` §6.4: read dot, kind tag, source, title, relative age. Adapt
`model.Item` to `list.Item`.

```go
type row struct{ model.Item }
func (r row) FilterValue() string { return r.Title }

type delegate struct{ styles styles.Styles }
func (d delegate) Render(w io.Writer, m list.Model, index int, li list.Item) {
    it := li.(row).Item
    dot := "○"; rowStyle := d.styles.Read
    if !it.Read { dot = "●"; rowStyle = d.styles.Unread }
    line := fmt.Sprintf("%s %-5s %-18s %s  %s",
        dot, it.Kind, truncate(it.SourceName, 18), truncate(it.Title, m.Width()-40), humanAge(it.Published))
    fmt.Fprint(w, rowStyle.Render(line))
}

type Model struct{ list list.Model }
func (m *Model) SetItems(items []model.Item) { /* map to []list.Item, list.SetItems */ }
func (m Model) Selected() (model.Item, bool) { /* list.SelectedItem() */ }
```

### Reader (`internal/ui/reader/reader.go`)

`bubbles/viewport` for the body; a lipgloss header for From/Title/Source/date.

```go
type Model struct {
    vp     viewport.Model
    header string
    styles styles.Styles
}
func (m *Model) SetContent(rendered string) { m.vp.SetContent(rendered); m.vp.GotoTop() }
func (m *Model) SetHeader(it model.Item)    { m.header = /* styled From/Title/Source/date */ }
func (m Model) View() string                { return m.header + "\n" + m.vp.View() }
```

### Keys & styles

```go
// keys/keys.go
type KeyMap struct{ Up, Down, Top, Bottom, Open, Back, Quit, Help key.Binding }
func Default() KeyMap // j/k, g/G, enter, esc, q, ?

// styles/styles.go
type Styles struct{ Unread, Read, KindTag, Header, StatusBar lipgloss.Style }
func DefaultStyles() Styles // Unread = bold/bright; Read = faint/dim
```

### Launch (`cmd/renomail/main.go`)

```go
func runTUI(cfg config.Config, paths config.Paths) error {
    st, err := store.Open(paths.DBFile); if err != nil { return err }
    defer st.Close()
    m, err := ui.New(st); if err != nil { return err }
    _, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
    return err
}
```

(`dump` from step 03 remains available as a subcommand.)

## Implementation flow

1. Add the Charm + html-to-markdown deps.
2. Implement `render` + tests.
3. Implement `styles`, `keys`.
4. Implement `feed` (delegate + list wrapper), then `reader`.
5. Implement `messages`, then `app` (router, key handling, sizing).
6. Wire `main.go` to launch the TUI by default.
7. Tests (`Update()` + teatest) + `gofmt`/`vet`/`build`/`test`.

## Validation criteria

- `go build ./...`, `go vet`, `gofmt` clean.
- **Render golden tests:** representative HTML (headings, list, link, code,
  `<script>` to confirm it is dropped) → stable golden output; empty-HTML item
  renders its `BodyText`.
- **`Update()` tests:** `itemsLoadedMsg` populates the feed; `Open` switches to
  `viewReader` and emits a body-load command; `Back` returns to `viewFeed`;
  `WindowSizeMsg` resizes children without panic.
- **`teatest` snapshot:** seed a temp store with a few items, drive the program,
  assert the feed view contains the titles and the read/unread dots.
- **Manual smoke:** after a `dump`, run `renomail`; arrow through items, open one,
  confirm rich rendering and scrolling, Esc returns.

## Done checklist

- [ ] Render pipeline converts HTML→markdown→styled text, width-aware, text
      fallback, strips scripts.
- [ ] Feed list renders rows with read/unread styling; reader scrolls rendered
      bodies; routing (open/back) works.
- [ ] TUI launches by default and displays cached items.
- [ ] Render goldens + `Update()`/teatest tests pass; `gofmt`/`vet`/`test` green.
