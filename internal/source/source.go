// Package source defines the Provider interface that the Gmail and RSS sources
// implement to fetch items into the store. A single interface lets the sync
// engine treat every origin uniformly.
package source

import (
	"context"
	"time"

	"github.com/srhoton/renomail/internal/model"
)

// Provider is one configured origin (a Gmail account or an RSS feed) that can
// produce items for the unified feed.
type Provider interface {
	// ID is the stable source identifier (account email or feed URL hash).
	ID() string
	// Name is the human-readable display name shown in the UI.
	Name() string
	// Kind reports whether this provider yields email or RSS items.
	Kind() model.Kind
	// Fetch returns items at or after since (the source's LastSync), already
	// populated for the list view (headers + snippet). Body may be empty and is
	// loaded lazily via Body.
	Fetch(ctx context.Context, since time.Time) ([]model.Item, error)
	// Body lazily loads the full content for one item, mutating it in place.
	// Called when the reader opens an item whose body is not yet cached.
	Body(ctx context.Context, item *model.Item) error
}

// ReadSyncer is implemented by providers that can reflect a local read-state change
// back to the originating account: Gmail toggles the UNREAD label via the API, Apple
// Mail sets a message's read status in Mail.app via AppleScript. Providers without it
// (RSS) are simply never asked. It is an optional capability, detected with a type
// assertion exactly like Stateful, so the core read-only Provider contract is
// unchanged. Write-back is best-effort: a returned error is surfaced to the user but
// never rolls back the authoritative local read state.
type ReadSyncer interface {
	// SetRead reflects the read flag for the given native ids (Item.NativeID) at the
	// source. A single toggle passes one id; Mark All Read passes a batch. ids the
	// provider cannot address are skipped. An empty slice is a no-op.
	SetRead(ctx context.Context, nativeIDs []string, read bool) error
}

// Stateful is implemented by providers that carry refreshed sync bookkeeping the
// caller should persist after a fetch (RSS exposes its updated ETag/Last-Modified
// via SourceState). Providers without it — Gmail — get a minimal Source recorded
// with just the current LastSync.
type Stateful interface {
	SourceState() model.Source
}

// StateOf returns the model.Source to persist for p after a sweep at now: the
// provider's own refreshed state when it implements Stateful (RSS), otherwise a
// minimal record carrying id, name, kind, and LastSync (Gmail). Shared by the
// dump command and the sync engine so per-source persistence stays uniform.
func StateOf(p Provider, now time.Time) model.Source {
	if ss, ok := p.(Stateful); ok {
		src := ss.SourceState()
		src.LastSync = now
		return src
	}
	return model.Source{ID: p.ID(), Name: p.Name(), Kind: p.Kind(), LastSync: now}
}
