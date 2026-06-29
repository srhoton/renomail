package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/srhoton/renomail/internal/model"
)

// newTestStore opens a fresh store backed by a temp-dir database file and
// registers cleanup.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// at builds a time at a fixed offset (whole seconds) for deterministic
// published-ordering assertions.
func at(unix int64) time.Time { return time.Unix(unix, 0).UTC() }

func newItem(id, srcID string, kind model.Kind, published int64) model.Item {
	return model.Item{
		ID:         id,
		Kind:       kind,
		SourceID:   srcID,
		SourceName: srcID + " name",
		Author:     "author-" + id,
		Title:      "title-" + id,
		Snippet:    "snippet-" + id,
		URL:        "https://example.test/" + id,
		NativeID:   "native-" + id,
		Published:  at(published),
		Fetched:    at(published + 1),
		BodyHTML:   "<p>" + id + "</p>",
		BodyText:   "body " + id,
	}
}

func TestQuery_ordersByPublishedDescending(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	items := []model.Item{
		newItem("a", "s1", model.KindRSS, 100),
		newItem("c", "s1", model.KindRSS, 300),
		newItem("b", "s1", model.KindRSS, 200),
	}
	if _, err := s.UpsertItems(ctx, items); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}

	got, err := s.Query(ctx, model.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := []string{"c", "b", "a"}
	if g := itemIDs(got); !slices.Equal(g, want) {
		t.Errorf("order = %v, want %v", g, want)
	}
}

func TestCount_byKindAndReadState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mailRead := newItem("m-read", "gmail", model.KindEmail, 100)
	mailRead.Read = true
	mailUnread1 := newItem("m-u1", "gmail", model.KindEmail, 200)
	mailUnread2 := newItem("m-u2", "gmail", model.KindEmail, 300)
	rssUnread := newItem("r-u1", "feed", model.KindRSS, 400)
	if _, err := s.UpsertItems(ctx, []model.Item{mailRead, mailUnread1, mailUnread2, rssUnread}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}

	tests := []struct {
		name   string
		filter model.Filter
		want   int
	}{
		{name: "all items", filter: model.Filter{}, want: 4},
		{
			name:   "unread emails",
			filter: model.Filter{Kinds: map[model.Kind]bool{model.KindEmail: true}, Read: model.ReadUnreadOnly},
			want:   2,
		},
		{
			name:   "unread rss",
			filter: model.Filter{Kinds: map[model.Kind]bool{model.KindRSS: true}, Read: model.ReadUnreadOnly},
			want:   1,
		},
		{
			name:   "read emails",
			filter: model.Filter{Kinds: map[model.Kind]bool{model.KindEmail: true}, Read: model.ReadReadOnly},
			want:   1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Count(ctx, tt.filter)
			if err != nil {
				t.Fatalf("Count: %v", err)
			}
			if got != tt.want {
				t.Errorf("Count(%+v) = %d, want %d", tt.filter, got, tt.want)
			}
		})
	}
}

func TestQuery_roundTripsAllFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	in := newItem("x", "s1", model.KindEmail, 500)
	if _, err := s.UpsertItems(ctx, []model.Item{in}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	got, err := s.Query(ctx, model.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1", len(got))
	}
	out := got[0]
	if out.NativeID != in.NativeID {
		t.Errorf("NativeID = %q, want %q", out.NativeID, in.NativeID)
	}
	if !out.Published.Equal(in.Published) {
		t.Errorf("Published = %v, want %v", out.Published, in.Published)
	}
	if !out.Fetched.Equal(in.Fetched) {
		t.Errorf("Fetched = %v, want %v", out.Fetched, in.Fetched)
	}
	if out.Kind != in.Kind || out.Author != in.Author || out.Title != in.Title {
		t.Errorf("scalar fields mismatch: got %+v", out)
	}
	if out.Read {
		t.Errorf("Read = true, want false for fresh item")
	}
}

func TestUpsertItems_preservesReadOnReFetch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	orig := newItem("a", "s1", model.KindRSS, 100)
	if _, err := s.UpsertItems(ctx, []model.Item{orig}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	if err := s.SetRead(ctx, "a", true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}

	// Re-fetch the same id with fresh content; Read is sent as false (the
	// provider has no knowledge of local state).
	refetched := newItem("a", "s1", model.KindRSS, 100)
	refetched.Title = "updated title"
	refetched.Snippet = "updated snippet"
	refetched.Read = false
	if _, err := s.UpsertItems(ctx, []model.Item{refetched}); err != nil {
		t.Fatalf("re-UpsertItems: %v", err)
	}

	got, err := s.Query(ctx, model.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1", len(got))
	}
	if got[0].Title != "updated title" {
		t.Errorf("Title = %q, want updated", got[0].Title)
	}
	if !got[0].Read {
		t.Errorf("Read flag was reset by re-fetch; want preserved true")
	}
}

func TestUpsertItems_emptyBodyDoesNotClobber(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	full := newItem("a", "s1", model.KindRSS, 100)
	if _, err := s.UpsertItems(ctx, []model.Item{full}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}

	stub := newItem("a", "s1", model.KindRSS, 100)
	stub.BodyHTML = ""
	stub.BodyText = ""
	if _, err := s.UpsertItems(ctx, []model.Item{stub}); err != nil {
		t.Fatalf("re-UpsertItems: %v", err)
	}

	html, text, err := s.GetBody(ctx, "a")
	if err != nil {
		t.Fatalf("GetBody: %v", err)
	}
	if html != full.BodyHTML || text != full.BodyText {
		t.Errorf("body clobbered by empty re-fetch: html=%q text=%q", html, text)
	}
}

func TestQuery_filterDimensions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seed := []model.Item{
		newItem("rss1", "feedA", model.KindRSS, 100),
		newItem("rss2", "feedB", model.KindRSS, 200),
		newItem("mail1", "acct", model.KindEmail, 300),
	}
	if _, err := s.UpsertItems(ctx, seed); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	// "mail1" title carries a searchable token.
	seed[2].Title = "quarterly report"
	if _, err := s.UpsertItems(ctx, []model.Item{seed[2]}); err != nil {
		t.Fatalf("UpsertItems update: %v", err)
	}
	if err := s.SetRead(ctx, "rss1", true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}

	tests := []struct {
		name   string
		filter model.Filter
		want   []string
	}{
		{
			name:   "by kind email",
			filter: model.Filter{Kinds: map[model.Kind]bool{model.KindEmail: true}},
			want:   []string{"mail1"},
		},
		{
			name:   "by kind rss",
			filter: model.Filter{Kinds: map[model.Kind]bool{model.KindRSS: true}},
			want:   []string{"rss2", "rss1"},
		},
		{
			name:   "by source id",
			filter: model.Filter{SourceIDs: map[string]bool{"feedB": true}},
			want:   []string{"rss2"},
		},
		{
			name:   "by multiple source ids",
			filter: model.Filter{SourceIDs: map[string]bool{"feedA": true, "acct": true}},
			want:   []string{"mail1", "rss1"},
		},
		{
			name:   "unread only",
			filter: model.Filter{Read: model.ReadUnreadOnly},
			want:   []string{"mail1", "rss2"},
		},
		{
			name:   "read only",
			filter: model.Filter{Read: model.ReadReadOnly},
			want:   []string{"rss1"},
		},
		{
			name:   "search title",
			filter: model.Filter{Search: "quarterly"},
			want:   []string{"mail1"},
		},
		{
			name: "combined kind + unread",
			filter: model.Filter{
				Kinds: map[model.Kind]bool{model.KindRSS: true},
				Read:  model.ReadUnreadOnly,
			},
			want: []string{"rss2"},
		},
		{
			name:   "multi-value kind IN clause",
			filter: model.Filter{Kinds: map[model.Kind]bool{model.KindRSS: true, model.KindEmail: true}},
			want:   []string{"mail1", "rss2", "rss1"},
		},
		{
			name: "combined search + kind",
			filter: model.Filter{
				Kinds:  map[model.Kind]bool{model.KindRSS: true},
				Search: "title-rss",
			},
			want: []string{"rss2", "rss1"},
		},
		{
			name: "combined search + source + unread",
			filter: model.Filter{
				SourceIDs: map[string]bool{"feedB": true},
				Read:      model.ReadUnreadOnly,
				Search:    "title-rss",
			},
			want: []string{"rss2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Query(ctx, tt.filter)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if g := itemIDs(got); !slices.Equal(g, tt.want) {
				t.Errorf("ids = %v, want %v", g, tt.want)
			}
		})
	}
}

func TestSetRead_togglesBothDirections(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.UpsertItems(ctx, []model.Item{newItem("a", "s1", model.KindRSS, 100)}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}

	if err := s.SetRead(ctx, "a", true); err != nil {
		t.Fatalf("SetRead(true): %v", err)
	}
	if got, _ := s.Query(ctx, model.Filter{Read: model.ReadReadOnly}); len(got) != 1 {
		t.Fatalf("after SetRead(true): read count = %d, want 1", len(got))
	}

	// Un-read it.
	if err := s.SetRead(ctx, "a", false); err != nil {
		t.Fatalf("SetRead(false): %v", err)
	}
	read, _ := s.Query(ctx, model.Filter{Read: model.ReadReadOnly})
	unread, _ := s.Query(ctx, model.Filter{Read: model.ReadUnreadOnly})
	if len(read) != 0 {
		t.Errorf("after SetRead(false): read count = %d, want 0", len(read))
	}
	if g := itemIDs(unread); !slices.Equal(g, []string{"a"}) {
		t.Errorf("after SetRead(false): unread = %v, want [a]", g)
	}
}

func TestQuery_searchEscapesWildcards(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	literal := newItem("lit", "s1", model.KindRSS, 200)
	literal.Title = "discount 50% off"
	other := newItem("oth", "s1", model.KindRSS, 100)
	other.Title = "plain title"
	if _, err := s.UpsertItems(ctx, []model.Item{literal, other}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}

	// "50%" must match the literal "50%" only, not act as "50" + wildcard.
	got, err := s.Query(ctx, model.Filter{Search: "50%"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if g := itemIDs(got); !slices.Equal(g, []string{"lit"}) {
		t.Errorf("search %q = %v, want [lit]", "50%", g)
	}

	// A wildcard-only term must not match everything; "%" is now a literal.
	got, err = s.Query(ctx, model.Filter{Search: "%"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if g := itemIDs(got); !slices.Equal(g, []string{"lit"}) {
		t.Errorf("search %q matched %v, want only the literal-percent row [lit]", "%", g)
	}
}

func TestMarkAllRead_onlyAffectsMatchingSubset(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seed := []model.Item{
		newItem("rss1", "feedA", model.KindRSS, 100),
		newItem("rss2", "feedA", model.KindRSS, 200),
		newItem("mail1", "acct", model.KindEmail, 300),
	}
	if _, err := s.UpsertItems(ctx, seed); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}

	// Mark only RSS read.
	if err := s.MarkAllRead(ctx, model.Filter{Kinds: map[model.Kind]bool{model.KindRSS: true}}); err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}

	stillUnread, err := s.Query(ctx, model.Filter{Read: model.ReadUnreadOnly})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if g := itemIDs(stillUnread); !slices.Equal(g, []string{"mail1"}) {
		t.Errorf("unread after MarkAllRead(rss) = %v, want [mail1]", g)
	}
}

func TestMarkAllRead_scopedBySourceID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seed := []model.Item{
		newItem("a1", "feedA", model.KindRSS, 100),
		newItem("a2", "feedA", model.KindRSS, 200),
		newItem("b1", "feedB", model.KindRSS, 300),
	}
	if _, err := s.UpsertItems(ctx, seed); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}

	// Mark every item from feedA read, leaving other sources untouched.
	if err := s.MarkAllRead(ctx, model.Filter{SourceIDs: map[string]bool{"feedA": true}}); err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}

	read, err := s.Query(ctx, model.Filter{Read: model.ReadReadOnly})
	if err != nil {
		t.Fatalf("Query(read): %v", err)
	}
	if g := itemIDs(read); !slices.Equal(g, []string{"a2", "a1"}) {
		t.Errorf("read after MarkAllRead(feedA) = %v, want feedA's items [a2 a1]", g)
	}

	unread, err := s.Query(ctx, model.Filter{Read: model.ReadUnreadOnly})
	if err != nil {
		t.Fatalf("Query(unread): %v", err)
	}
	if g := itemIDs(unread); !slices.Equal(g, []string{"b1"}) {
		t.Errorf("unread after MarkAllRead(feedA) = %v, want [b1]", g)
	}
}

func TestSetGetBody_roundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	it := newItem("a", "s1", model.KindRSS, 100)
	it.BodyHTML, it.BodyText = "", ""
	if _, err := s.UpsertItems(ctx, []model.Item{it}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	if err := s.SetBody(ctx, "a", "<h1>hi</h1>", "hi"); err != nil {
		t.Fatalf("SetBody: %v", err)
	}
	html, text, err := s.GetBody(ctx, "a")
	if err != nil {
		t.Fatalf("GetBody: %v", err)
	}
	if html != "<h1>hi</h1>" || text != "hi" {
		t.Errorf("body round-trip = (%q,%q)", html, text)
	}
}

func TestGetBody_missingItem(t *testing.T) {
	s := newTestStore(t)
	_, _, err := s.GetBody(context.Background(), "nope")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetBody(missing) error = %v, want sql.ErrNoRows", err)
	}
}

func TestUpsertGetSource_roundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, ok, err := s.GetSource(ctx, "feedA")
	if err != nil {
		t.Fatalf("GetSource(absent): %v", err)
	}
	if ok {
		t.Fatalf("GetSource(absent) ok = true, want false")
	}

	src := model.Source{
		ID:           "feedA",
		Name:         "Feed A",
		Kind:         model.KindRSS,
		LastSync:     at(12345),
		ETag:         `W/"abc"`,
		LastModified: "Wed, 21 Oct 2015 07:28:00 GMT",
	}
	if err := s.UpsertSource(ctx, src); err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}

	got, ok, err := s.GetSource(ctx, "feedA")
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if !ok {
		t.Fatalf("GetSource ok = false, want true")
	}
	if got.ID != src.ID || got.Name != src.Name || got.Kind != src.Kind ||
		got.ETag != src.ETag || got.LastModified != src.LastModified {
		t.Errorf("source mismatch: got %+v want %+v", got, src)
	}
	if !got.LastSync.Equal(src.LastSync) {
		t.Errorf("LastSync = %v, want %v", got.LastSync, src.LastSync)
	}

	// Upsert again to confirm conflict-update path refreshes every column.
	src.Name = "Feed A renamed"
	src.LastSync = at(99999)
	src.ETag = `W/"def"`
	src.LastModified = "Thu, 22 Oct 2015 07:28:00 GMT"
	if err := s.UpsertSource(ctx, src); err != nil {
		t.Fatalf("UpsertSource update: %v", err)
	}
	got, _, err = s.GetSource(ctx, "feedA")
	if err != nil {
		t.Fatalf("GetSource after update: %v", err)
	}
	if got.Name != "Feed A renamed" || !got.LastSync.Equal(at(99999)) ||
		got.ETag != `W/"def"` || got.LastModified != "Thu, 22 Oct 2015 07:28:00 GMT" {
		t.Errorf("source not fully updated on conflict: got %+v", got)
	}
}

func TestUpsertSources_batchAndEmptyNoop(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.UpsertSources(ctx, nil); err != nil {
		t.Fatalf("UpsertSources(nil) = %v, want nil no-op", err)
	}

	srcs := []model.Source{
		{ID: "rss:a", Name: "A", Kind: model.KindRSS, LastSync: at(100), ETag: `"a"`},
		{ID: "gmail:b", Name: "b", Kind: model.KindEmail, LastSync: at(200)},
	}
	if err := s.UpsertSources(ctx, srcs); err != nil {
		t.Fatalf("UpsertSources: %v", err)
	}

	for _, want := range srcs {
		got, ok, err := s.GetSource(ctx, want.ID)
		if err != nil || !ok {
			t.Fatalf("GetSource(%s) ok=%v err=%v", want.ID, ok, err)
		}
		if got.Name != want.Name || got.Kind != want.Kind || got.ETag != want.ETag ||
			!got.LastSync.Equal(want.LastSync) {
			t.Errorf("source %s = %+v, want %+v", want.ID, got, want)
		}
	}

	// A second batch must update existing rows (conflict path).
	srcs[0].Name = "A renamed"
	srcs[0].LastSync = at(999)
	if err := s.UpsertSources(ctx, srcs[:1]); err != nil {
		t.Fatalf("UpsertSources update: %v", err)
	}
	got, _, _ := s.GetSource(ctx, "rss:a")
	if got.Name != "A renamed" || !got.LastSync.Equal(at(999)) {
		t.Errorf("batch conflict update failed: %+v", got)
	}
}

// itemIDs extracts the ids from a slice of items, preserving order.
func itemIDs(items []model.Item) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

func TestUpsertItems_emptySliceNoop(t *testing.T) {
	s := newTestStore(t)
	got, err := s.UpsertItems(context.Background(), nil)
	if err != nil {
		t.Errorf("UpsertItems(nil) = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("UpsertItems(nil) new items = %d, want 0", len(got))
	}
}

func TestUpsertItems_returnsNewItems(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// First batch: every id is new, so all are returned, in input order.
	first := []model.Item{
		newItem("a", "s1", model.KindRSS, 100),
		newItem("b", "s1", model.KindRSS, 200),
		newItem("c", "s1", model.KindRSS, 300),
	}
	got, err := s.UpsertItems(ctx, first)
	if err != nil {
		t.Fatalf("UpsertItems(first): %v", err)
	}
	if diff := itemIDs(got); !slices.Equal(diff, []string{"a", "b", "c"}) {
		t.Errorf("first new ids = %v, want [a b c]", diff)
	}

	// Re-upsert the same ids: none are new, so the result is empty even though the
	// rows are updated in place.
	for i := range first {
		first[i].Title = "refreshed " + first[i].ID
	}
	got, err = s.UpsertItems(ctx, first)
	if err != nil {
		t.Fatalf("UpsertItems(re-seen): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("re-seen new items = %v, want none", itemIDs(got))
	}

	// Mixed batch: two known ids plus one new id returns exactly the new one.
	mixed := []model.Item{
		newItem("a", "s1", model.KindRSS, 100), // existing
		newItem("c", "s1", model.KindRSS, 300), // existing
		newItem("d", "s1", model.KindRSS, 400), // new
	}
	got, err = s.UpsertItems(ctx, mixed)
	if err != nil {
		t.Fatalf("UpsertItems(mixed): %v", err)
	}
	if ids := itemIDs(got); !slices.Equal(ids, []string{"d"}) {
		t.Errorf("mixed new ids = %v, want [d]", ids)
	}

	// The content refresh from the re-seen batch must still have landed.
	all, err := s.Query(ctx, model.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("row count = %d, want 4", len(all))
	}
}

func TestUpsertItems_dedupesRepeatedIDsInBatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// A batch carrying the same id twice (e.g. two RSS entries that collapse to the
	// same StableID) inserts a single row, so it is returned once, not twice.
	dup := newItem("a", "s1", model.KindRSS, 100)
	batch := []model.Item{dup, newItem("a", "s1", model.KindRSS, 100)}
	got, err := s.UpsertItems(ctx, batch)
	if err != nil {
		t.Fatalf("UpsertItems(dup): %v", err)
	}
	if ids := itemIDs(got); !slices.Equal(ids, []string{"a"}) {
		t.Errorf("new ids = %v, want [a] (the repeated id appears once)", ids)
	}

	// Exactly one row landed.
	all, err := s.Query(ctx, model.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("row count = %d, want 1", len(all))
	}

	// A mixed batch: one new id repeated, plus the now-existing id — one genuine new item.
	mixed := []model.Item{
		newItem("b", "s1", model.KindRSS, 200),
		newItem("b", "s1", model.KindRSS, 200), // duplicate of the new id
		newItem("a", "s1", model.KindRSS, 100), // already stored
	}
	got, err = s.UpsertItems(ctx, mixed)
	if err != nil {
		t.Fatalf("UpsertItems(mixed dup): %v", err)
	}
	if ids := itemIDs(got); !slices.Equal(ids, []string{"b"}) {
		t.Errorf("mixed new ids = %v, want [b]", ids)
	}
}

func TestUpsertItems_spansChunkBoundary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// More than one existence-check chunk (500) of brand-new ids: every chunk's new
	// ids must be returned, summing to the full total.
	const total = 1200
	batch := make([]model.Item, total)
	for i := range batch {
		batch[i] = newItem("item-"+strconv.Itoa(i), "s1", model.KindRSS, int64(i))
	}
	got, err := s.UpsertItems(ctx, batch)
	if err != nil {
		t.Fatalf("UpsertItems(large): %v", err)
	}
	if len(got) != total {
		t.Errorf("new items = %d, want %d", len(got), total)
	}

	// Re-upsert the same large batch: zero new across every chunk.
	got, err = s.UpsertItems(ctx, batch)
	if err != nil {
		t.Fatalf("UpsertItems(large re-seen): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("re-seen large new items = %d, want 0", len(got))
	}
}

func TestOpen_rejectsNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "newer.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE meta SET value = '999' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := Open(path); err == nil {
		t.Errorf("Open of newer-schema db = nil error, want refusal")
	}
}
