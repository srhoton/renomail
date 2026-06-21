package source_test

import (
	"context"
	"testing"
	"time"

	"github.com/srhoton/renomail/internal/model"
	"github.com/srhoton/renomail/internal/source"
)

// plainProvider implements source.Provider but not source.Stateful, standing in
// for Gmail (no per-source validators to persist).
type plainProvider struct{ id, name string }

func (p plainProvider) ID() string                                             { return p.id }
func (p plainProvider) Name() string                                           { return p.name }
func (p plainProvider) Kind() model.Kind                                       { return model.KindEmail }
func (p plainProvider) Fetch(context.Context, time.Time) ([]model.Item, error) { return nil, nil }
func (p plainProvider) Body(context.Context, *model.Item) error                { return nil }

// statefulProvider adds SourceState, standing in for RSS (carries ETag etc.).
type statefulProvider struct {
	plainProvider
	state model.Source
}

func (p statefulProvider) SourceState() model.Source { return p.state }

func TestStateOf_plainProvider_minimalRecord(t *testing.T) {
	now := time.Now().UTC()
	p := plainProvider{id: "gmail:me", name: "me"}

	got := source.StateOf(p, now)

	want := model.Source{ID: "gmail:me", Name: "me", Kind: model.KindEmail, LastSync: now}
	if got != want {
		t.Errorf("StateOf = %+v, want %+v", got, want)
	}
}

func TestStateOf_statefulProvider_usesSourceStateWithLastSync(t *testing.T) {
	now := time.Now().UTC()
	p := statefulProvider{
		plainProvider: plainProvider{id: "rss:x", name: "X"},
		state: model.Source{
			ID: "rss:x", Name: "X", Kind: model.KindRSS,
			ETag: `"v1"`, LastModified: "Mon, 01 Jan 2024 00:00:00 GMT",
			LastSync: now.Add(-time.Hour), // should be overwritten with now
		},
	}

	got := source.StateOf(p, now)

	if got.ETag != `"v1"` || got.LastModified == "" || got.Kind != model.KindRSS {
		t.Errorf("StateOf dropped the provider's validators: %+v", got)
	}
	if !got.LastSync.Equal(now) {
		t.Errorf("LastSync = %v, want overwritten to now %v", got.LastSync, now)
	}
}
