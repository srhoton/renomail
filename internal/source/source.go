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
