// Package model holds renomail's domain types: the unified Item, its Source and
// Kind, and the Filter used to query the feed. These types are the shared
// vocabulary between the source providers, the store, and the UI.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"time"
)

// Kind distinguishes the origin family of an Item.
type Kind string

const (
	// KindEmail marks an Item that originated from a Gmail message.
	KindEmail Kind = "email"
	// KindRSS marks an Item that originated from an RSS/Atom feed entry.
	KindRSS Kind = "rss"
)

// ReadState selects which items a Filter matches by their local read flag.
type ReadState int

const (
	// ReadAny matches items regardless of read state.
	ReadAny ReadState = iota
	// ReadUnreadOnly matches only unread items.
	ReadUnreadOnly
	// ReadReadOnly matches only read items.
	ReadReadOnly
)

// Item is the unified feed unit: both an email and an RSS entry map onto it.
type Item struct {
	ID         string    // stable: sha256(sourceID + nativeID)
	Kind       Kind      // email or rss
	SourceID   string    // account id ("me@gmail.com") or feed id (feed URL hash)
	SourceName string    // display name: "Personal Gmail" / "Hacker News"
	Author     string    // From header / feed entry author
	Title      string    // Subject / entry title
	Snippet    string    // short preview for the list row
	URL        string    // permalink (RSS) or Gmail web deep-link (email)
	NativeID   string    // provider's native id: Gmail message id / RSS guid|link
	Published  time.Time // sort key (desc)
	Fetched    time.Time // when this item was last fetched
	Read       bool      // LOCAL state only; never synced back to the source
	BodyHTML   string    // lazily populated for email; usually present for RSS
	BodyText   string    // fallback / search
}

// Source is a configured origin: one Gmail account or one feed.
type Source struct {
	ID   string
	Name string
	Kind Kind
	// sync bookkeeping:
	LastSync     time.Time
	ETag         string // RSS conditional GET
	LastModified string // RSS conditional GET
}

// Filter drives both the SQL query and the visible feed. A zero Filter
// (no kinds, no sources, ReadAny, empty search) matches every item.
type Filter struct {
	Kinds     map[Kind]bool   // empty/nil = all kinds
	SourceIDs map[string]bool // empty/nil = all sources
	Read      ReadState
	Search    string // matches Title/Author/Snippet/BodyText
}

// StableID derives a deterministic Item.ID from the source and the provider's
// native id (Gmail message id / RSS entry guid|link). The same inputs always
// produce the same id, which is what makes upserts idempotent. The NUL
// separator prevents distinct (sourceID, nativeID) pairs from colliding, e.g.
// ("ab","c") versus ("a","bc").
func StableID(sourceID, nativeID string) string {
	h := sha256.New()
	_, _ = io.WriteString(h, sourceID)
	_, _ = h.Write([]byte{0})
	_, _ = io.WriteString(h, nativeID)
	return hex.EncodeToString(h.Sum(nil))
}
