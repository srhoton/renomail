// Package feed renders the unified feed list and its item delegate: one row per
// item with a read/unread dot, kind tag, source, title, and relative age
// (DESIGN.md §6.4). It wraps bubbles/list and adapts model.Item onto list.Item.
package feed

import (
	"io"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/dustin/go-humanize"
	runewidth "github.com/mattn/go-runewidth"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/ui/styles"
)

const (
	kindWidth    = 5  // fixed column for the kind tag ("email" is the widest)
	sourceWidth  = 18 // fixed column for the source name
	minTitleCols = 12 // never squeeze the title below this
	// fixedCols is the display width of every column except the flexible title
	// and the trailing age: cursor(2) + dot(1) + space + kind + space + source +
	// space + 2-space gap before age.
	fixedCols = 2 + 1 + 1 + kindWidth + 1 + sourceWidth + 1 + 2
)

// row adapts a model.Item onto the list.Item interface. The per-frame-invariant
// columns (kind tag, padded source, relative age) are precomputed once at
// construction so the delegate's Render — called per visible row on every update —
// does no formatting or time lookups; only the width-dependent title and the
// read-state dot are computed there.
type row struct {
	item   model.Item
	kind   string // string(item.Kind), truncated+padded to kindWidth cells
	source string // item.SourceName, truncated+padded to sourceWidth cells
	age    string // relative age ("3h ago") as of load time
}

// makeRow precomputes the static display columns for an item.
func makeRow(it model.Item) row {
	return row{
		item:   it,
		kind:   runewidth.FillRight(runewidth.Truncate(string(it.Kind), kindWidth, ""), kindWidth),
		source: runewidth.FillRight(truncate(it.SourceName, sourceWidth), sourceWidth),
		age:    humanize.Time(it.Published),
	}
}

// FilterValue is what bubbles/list filters against; the title is the natural key.
func (r row) FilterValue() string { return r.item.Title }

// delegate renders a single feed row. It is a value type holding the theme so the
// list can render rows without reaching back into the parent model.
type delegate struct {
	styles styles.Styles
}

func (d delegate) Height() int                             { return 1 }
func (d delegate) Spacing() int                            { return 0 }
func (d delegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

// Render draws one row: "> ● rss   Hacker News      Some title            3h ago".
// Unread rows use the bright/bold style and a filled dot; read rows are dimmed
// with a hollow dot (DESIGN.md §6.4). Only the read-state dot and the
// width-flexed title are computed here; the other columns are precomputed on the
// row, so this stays allocation-light on the per-frame path.
func (d delegate) Render(w io.Writer, m list.Model, index int, li list.Item) {
	r, ok := li.(row)
	if !ok {
		return
	}

	dot, style := "○", d.styles.Read
	if !r.item.Read {
		dot, style = "●", d.styles.Unread
	}
	cursor := "  "
	if index == m.Index() {
		cursor = "> "
	}

	// The title flexes to fill whatever the fixed columns and the age leave.
	titleCols := max(m.Width()-fixedCols-runewidth.StringWidth(r.age), minTitleCols)
	title := runewidth.FillRight(truncate(r.item.Title, titleCols), titleCols)

	// Exact final size: every component's byte length plus the 5 separator bytes
	// written below (three single spaces and one two-space gap).
	const separatorBytes = 5
	var b strings.Builder
	b.Grow(len(cursor) + len(dot) + len(r.kind) + len(r.source) + len(title) + len(r.age) + separatorBytes)
	b.WriteString(cursor)
	b.WriteString(dot)
	b.WriteByte(' ')
	b.WriteString(r.kind)
	b.WriteByte(' ')
	b.WriteString(r.source)
	b.WriteByte(' ')
	b.WriteString(title)
	b.WriteString("  ")
	b.WriteString(r.age)
	_, _ = io.WriteString(w, style.Render(b.String()))
}

// Model wraps a bubbles/list configured as a full-screen, chrome-free feed.
type Model struct {
	list list.Model
}

// New builds an empty feed list with the custom row delegate. The list's own
// title/status/help/pagination/filtering chrome is disabled: the root model owns
// the status line, and search arrives in a later step.
func New(st styles.Styles) Model {
	l := list.New(nil, delegate{styles: st}, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetShowFilter(false)
	l.SetFilteringEnabled(false)
	return Model{list: l}
}

// SetItems replaces the feed contents, precomputing each row's static columns.
// The returned command is from the underlying list and should be returned to the
// Bubble Tea runtime by the caller.
func (m *Model) SetItems(items []model.Item) tea.Cmd {
	rows := make([]list.Item, len(items))
	for i, it := range items {
		rows[i] = makeRow(it)
	}
	return m.list.SetItems(rows)
}

// SetSize resizes the underlying list to the available area.
func (m *Model) SetSize(w, h int) { m.list.SetSize(w, h) }

// Selected returns the currently highlighted item, or false when the feed is
// empty.
func (m Model) Selected() (model.Item, bool) {
	r, ok := m.list.SelectedItem().(row)
	if !ok {
		return model.Item{}, false
	}
	return r.item, true
}

// SetReadLocal flips the read flag of the row with the given id in place, so its
// dot and dimming update immediately without a full re-query. It addresses the row
// by id (rather than by selection) so a persisted read confirmation can be
// reflected even if the selection has since moved. Unknown ids are a no-op. The
// precomputed columns are unaffected by the read flag, so the row is reused.
func (m *Model) SetReadLocal(id string, read bool) {
	for i, li := range m.list.Items() {
		r, ok := li.(row)
		if !ok || r.item.ID != id {
			continue
		}
		r.item.Read = read
		m.list.SetItem(i, r)
		return
	}
}

// RemoveLocal drops the row with the given id from the list in place, so an item
// that has left the active read filter (e.g. marked read under an unread-only
// filter) disappears immediately without re-querying the whole feed. It addresses
// the row by id for the same stale-safe reason as SetReadLocal; unknown ids are a
// no-op.
func (m *Model) RemoveLocal(id string) {
	for i, li := range m.list.Items() {
		r, ok := li.(row)
		if !ok || r.item.ID != id {
			continue
		}
		m.list.RemoveItem(i)
		return
	}
}

// Update forwards messages (motion keys, resize) to the embedded list.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// View renders the list.
func (m Model) View() string { return m.list.View() }

// truncate shortens s to at most n display columns, appending an ellipsis when it
// is cut. It is display-width aware (wide/CJK runes count as two columns) so
// columns stay aligned and are never split mid-rune.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return runewidth.Truncate(s, n, "…")
}
