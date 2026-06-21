package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/render"
	"github.com/srhoton/renomail/internal/store"
)

// itemsLoadedMsg carries the result of a store query into the feed.
type itemsLoadedMsg struct{ items []model.Item }

// bodyLoadedMsg carries a rendered item body (or the error from loading it) into
// the reader.
type bodyLoadedMsg struct {
	id       string
	rendered string
	err      error
}

// errMsg reports a background failure (e.g. a failed query) to the status line.
type errMsg struct{ err error }

// readToggledMsg reports the outcome of persisting a single item's read flag. The
// caller flips the row optimistically; this confirms the persisted state (or
// surfaces the error) so the in-memory feed and the store stay in lockstep.
type readToggledMsg struct {
	id   string
	read bool
	err  error
}

// reloadMsg asks the root model to re-run the current filter query. It is emitted
// after a store mutation (e.g. mark-all-read) so the re-query happens strictly
// after the write completes, rather than racing it.
type reloadMsg struct{}

// loadItemsCmd queries the store off the UI goroutine and returns the matching
// items, newest first. A query failure becomes an errMsg so the UI can surface it
// without blocking.
func loadItemsCmd(st *store.Store, f model.Filter) tea.Cmd {
	return func() tea.Msg {
		items, err := st.Query(context.Background(), f)
		if err != nil {
			return errMsg{err}
		}
		return itemsLoadedMsg{items}
	}
}

// loadBodyCmd hydrates an item's body from the store and renders it. The list
// query returns body-less items (the store omits bodies from list columns), so
// the body must be fetched here before rendering — this is the lazy body-load the
// design intends, and the seam the Gmail provider will reuse for network-fetched
// bodies. A missing or already-present BodyText falls back gracefully.
func loadBodyCmd(st *store.Store, r *render.Renderer, it model.Item) tea.Cmd {
	return func() tea.Msg {
		html, text, err := st.GetBody(context.Background(), it.ID)
		if err != nil {
			return bodyLoadedMsg{id: it.ID, err: err}
		}
		it.BodyHTML, it.BodyText = html, text
		out, err := r.Render(it)
		return bodyLoadedMsg{id: it.ID, rendered: out, err: err}
	}
}

// setReadCmd persists an item's local read flag off the UI goroutine and reports
// the outcome as a readToggledMsg. The in-memory feed row is flipped
// optimistically by the caller; the returned message lets the model confirm the
// persisted state and surface any failure.
func setReadCmd(st *store.Store, id string, read bool) tea.Cmd {
	return func() tea.Msg {
		err := st.SetRead(context.Background(), id, read)
		return readToggledMsg{id: id, read: read, err: err}
	}
}

// markAllReadCmd marks every item matching f as read, then asks the model to
// re-query so the now-read items restyle (or drop out, under an unread-only
// filter). The reload is returned as a message rather than chained with
// tea.Sequence so it is guaranteed to run after the write.
func markAllReadCmd(st *store.Store, f model.Filter) tea.Cmd {
	return func() tea.Msg {
		if err := st.MarkAllRead(context.Background(), f); err != nil {
			return errMsg{err}
		}
		return reloadMsg{}
	}
}
