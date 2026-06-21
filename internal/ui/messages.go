package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/render"
	"github.com/srhoton/renomail/internal/source"
	"github.com/srhoton/renomail/internal/store"
	"github.com/srhoton/renomail/internal/syncengine"
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

// syncBatchMsg carries one provider's sync outcome from the background engine
// into the model. The engine has already upserted the items; the model only needs
// to surface the error or re-query the visible feed.
type syncBatchMsg struct{ res syncengine.Result }

// waitForActivity blocks on the engine's events channel and converts the next
// Result into a syncBatchMsg. The model re-arms it after each delivery so the
// stream is drained continuously without a global program reference. A closed
// channel (engine shut down) yields a nil msg, which stops the loop.
func waitForActivity(ch <-chan syncengine.Result) tea.Cmd {
	return func() tea.Msg {
		res, ok := <-ch
		if !ok {
			return nil
		}
		return syncBatchMsg{res}
	}
}

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

// loadBodyCmd hydrates an item's body and renders it. The list query returns
// body-less items (the store omits bodies from list columns), so the body is
// fetched here before rendering. It first reads the store; when the stored body
// is empty and a provider is known for the item (Gmail stores body-less rows on
// Fetch), it falls back to the provider's network Body load and caches the result
// via SetBody so the next open is instant. RSS bodies are already stored, so the
// fallback never fires for them. The cache write is best-effort — a failed cache
// must not block rendering the body we just fetched.
func loadBodyCmd(st *store.Store, r *render.Renderer, prov source.Provider, it model.Item) tea.Cmd {
	return func() tea.Msg {
		html, text, err := st.GetBody(context.Background(), it.ID)
		if err != nil {
			return bodyLoadedMsg{id: it.ID, err: err}
		}
		if html == "" && text == "" && prov != nil {
			if berr := prov.Body(context.Background(), &it); berr != nil {
				return bodyLoadedMsg{id: it.ID, err: berr}
			}
			html, text = it.BodyHTML, it.BodyText
			_ = st.SetBody(context.Background(), it.ID, html, text)
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
