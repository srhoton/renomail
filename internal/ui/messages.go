package ui

import (
	"context"
	"fmt"
	"time"

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

// readSyncedMsg reports the outcome of pushing a read-state change back to the source
// account (Gmail/Apple Mail). It is best-effort: a failure is shown on the status line
// but never rolls back the local read state, which is already persisted.
type readSyncedMsg struct{ err error }

// markedAllReadMsg carries the result of a mark-all-read store mutation: the native ids
// that were flipped from unread to read, grouped by source id, so the model can both
// re-query the feed and push the change back to each source that supports write-back.
type markedAllReadMsg struct {
	bySource map[string][]string
	err      error
}

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

// syncReadWriteTimeout bounds one best-effort write-back so a hung remote — a stalled
// Gmail call, or osascript blocked on a Mail.app modal / cold start — cannot leak the
// command goroutine (and its subprocess) for the life of the session. It is generous
// enough to cover a large Mark All Read batch.
const syncReadWriteTimeout = 60 * time.Second

// syncReadCmd pushes a read-state change for nativeIDs back to the source via its
// ReadSyncer, off the UI goroutine. It runs independently of (and never blocks) the
// local store write, so a slow or failing remote never stalls the UI; the outcome
// returns as a readSyncedMsg. A bounded context reaps a hung remote rather than leaking.
func syncReadCmd(rs source.ReadSyncer, nativeIDs []string, read bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), syncReadWriteTimeout)
		defer cancel()
		return readSyncedMsg{err: rs.SetRead(ctx, nativeIDs, read)}
	}
}

// notifyCmd pushes a "new items" host notification off the UI goroutine for one
// source's fresh arrivals. fn is the model's injected notifier (a no-op unless we
// are inside tmux); a notifier failure surfaces as an errMsg on the status line
// rather than crashing, mirroring openInBrowser. On success it yields a nil msg.
func notifyCmd(fn func(string) error, srcName string, n int) tea.Cmd {
	return func() tea.Msg {
		msg := fmt.Sprintf("renomail: %d new from %s", n, srcName)
		if err := fn(msg); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// markAllReadCmd marks every item matching f as read and reports which items flipped
// (the currently-unread ones within f, grouped by source) so the model can re-query the
// feed and push the change back to each write-back-capable source. The store is queried
// for those ids before the bulk update so write-back targets exactly the items that
// changed; an empty result still marks read and reloads. Running off the UI goroutine,
// the markedAllReadMsg is delivered strictly after the write, so the re-query never
// races it.
func markAllReadCmd(st *store.Store, f model.Filter) tea.Cmd {
	return func() tea.Msg {
		// The items that actually flip are those matching f AND currently unread. A
		// read-only filter matches no unread items, so nothing flips and there is nothing
		// to write back — query and push only when unread items can be in scope.
		var bySource map[string][]string
		if f.Read != model.ReadReadOnly {
			unread := f
			unread.Read = model.ReadUnreadOnly
			items, err := st.Query(context.Background(), unread)
			if err != nil {
				return markedAllReadMsg{err: err}
			}
			bySource = make(map[string][]string)
			for _, it := range items {
				if it.NativeID != "" {
					bySource[it.SourceID] = append(bySource[it.SourceID], it.NativeID)
				}
			}
		}
		if err := st.MarkAllRead(context.Background(), f); err != nil {
			return markedAllReadMsg{err: err}
		}
		return markedAllReadMsg{bySource: bySource}
	}
}
