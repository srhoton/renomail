package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gmailapi "google.golang.org/api/gmail/v1"
)

// captured holds the decoded batchModify requests an httptest Gmail server received,
// so a test can assert the label deltas, ids, and chunking the provider produced.
type captured struct {
	mu   sync.Mutex
	reqs []gmailapi.BatchModifyMessagesRequest
}

func (c *captured) add(r gmailapi.BatchModifyMessagesRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, r)
}

func (c *captured) snapshot() []gmailapi.BatchModifyMessagesRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.reqs)
}

// batchModifyService returns a provider whose Gmail service points at a server that
// records every batchModify call and replies 204, the API's success status.
func batchModifyService(t *testing.T, rec *captured) *Provider {
	t.Helper()
	svc := gmailTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages/batchModify") {
			http.NotFound(w, r)
			return
		}
		var req gmailapi.BatchModifyMessagesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rec.add(req)
		w.WriteHeader(http.StatusNoContent)
	})
	return newWithService("me@example.com", svc, time.Hour)
}

func TestSetRead_marksReadRemovesUnreadLabel(t *testing.T) {
	var rec captured
	p := batchModifyService(t, &rec)

	if err := p.SetRead(context.Background(), []string{"m1", "m2"}, true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}
	reqs := rec.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("got %d batchModify calls, want 1", len(reqs))
	}
	if got := reqs[0].RemoveLabelIds; len(got) != 1 || got[0] != "UNREAD" {
		t.Errorf("RemoveLabelIds = %v, want [UNREAD]", got)
	}
	if len(reqs[0].AddLabelIds) != 0 {
		t.Errorf("AddLabelIds = %v, want none when marking read", reqs[0].AddLabelIds)
	}
	if got := reqs[0].Ids; len(got) != 2 || got[0] != "m1" || got[1] != "m2" {
		t.Errorf("Ids = %v, want [m1 m2]", got)
	}
}

func TestSetRead_marksUnreadAddsUnreadLabel(t *testing.T) {
	var rec captured
	p := batchModifyService(t, &rec)

	if err := p.SetRead(context.Background(), []string{"m9"}, false); err != nil {
		t.Fatalf("SetRead: %v", err)
	}
	reqs := rec.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("got %d batchModify calls, want 1", len(reqs))
	}
	if got := reqs[0].AddLabelIds; len(got) != 1 || got[0] != "UNREAD" {
		t.Errorf("AddLabelIds = %v, want [UNREAD]", got)
	}
	if len(reqs[0].RemoveLabelIds) != 0 {
		t.Errorf("RemoveLabelIds = %v, want none when marking unread", reqs[0].RemoveLabelIds)
	}
}

func TestSetRead_chunksOverApiLimit(t *testing.T) {
	var rec captured
	p := batchModifyService(t, &rec)

	ids := make([]string, modifyBatchSize+5)
	for i := range ids {
		ids[i] = "m" + strconv.Itoa(i)
	}
	if err := p.SetRead(context.Background(), ids, true); err != nil {
		t.Fatalf("SetRead: %v", err)
	}
	reqs := rec.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("got %d batchModify calls, want 2 (chunked at %d)", len(reqs), modifyBatchSize)
	}
	if len(reqs[0].Ids) != modifyBatchSize || len(reqs[1].Ids) != 5 {
		t.Errorf("chunk sizes = %d, %d; want %d, 5", len(reqs[0].Ids), len(reqs[1].Ids), modifyBatchSize)
	}
}

func TestSetRead_emptyIsNoOp(t *testing.T) {
	var rec captured
	p := batchModifyService(t, &rec)

	for _, ids := range [][]string{nil, {}, {"", "  "}} {
		if err := p.SetRead(context.Background(), ids, true); err != nil {
			t.Fatalf("SetRead(%v): %v", ids, err)
		}
	}
	if reqs := rec.snapshot(); len(reqs) != 0 {
		t.Errorf("got %d batchModify calls for empty/blank id sets, want 0", len(reqs))
	}
}

func TestSetRead_insufficientScopeMapsToReauthorize(t *testing.T) {
	svc := gmailTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    403,
				"message": "Request had insufficient authentication scopes.",
				"errors": []map[string]any{
					{"reason": "insufficientPermissions", "message": "Insufficient Permission"},
				},
			},
		})
	})
	p := newWithService("me@example.com", svc, time.Hour)

	err := p.SetRead(context.Background(), []string{"m1"}, true)
	if !errors.Is(err, ErrReauthorize) {
		t.Fatalf("SetRead 403 error = %v, want ErrReauthorize", err)
	}
	if !strings.Contains(err.Error(), "me@example.com") {
		t.Errorf("error %q should name the account for an actionable hint", err)
	}
}

func TestSetRead_otherErrorIsNotReauthorize(t *testing.T) {
	svc := gmailTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": 500, "message": "backend error"},
		})
	})
	p := newWithService("me@example.com", svc, time.Hour)

	err := p.SetRead(context.Background(), []string{"m1"}, true)
	if err == nil {
		t.Fatal("SetRead 500 error = nil, want a wrapped failure")
	}
	if errors.Is(err, ErrReauthorize) {
		t.Errorf("a 500 should not map to ErrReauthorize: %v", err)
	}
}
