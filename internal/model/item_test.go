package model

import "testing"

func TestStableID_deterministic(t *testing.T) {
	a := StableID("me@gmail.com", "msg-123")
	b := StableID("me@gmail.com", "msg-123")
	if a != b {
		t.Fatalf("StableID not deterministic: %q != %q", a, b)
	}
	// sha256 hex is 64 characters.
	if len(a) != 64 {
		t.Errorf("StableID length = %d, want 64", len(a))
	}
}

func TestStableID_distinctInputs(t *testing.T) {
	tests := []struct {
		name           string
		srcA, natA     string
		srcB, natB     string
		wantDistinctID bool
	}{
		{
			name: "different native id",
			srcA: "s", natA: "1",
			srcB: "s", natB: "2",
			wantDistinctID: true,
		},
		{
			name: "different source id",
			srcA: "s1", natA: "x",
			srcB: "s2", natB: "x",
			wantDistinctID: true,
		},
		{
			name: "separator collision guard",
			srcA: "ab", natA: "c",
			srcB: "a", natB: "bc",
			wantDistinctID: true,
		},
		{
			name: "identical inputs",
			srcA: "s", natA: "n",
			srcB: "s", natB: "n",
			wantDistinctID: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := StableID(tt.srcA, tt.natA)
			b := StableID(tt.srcB, tt.natB)
			if got := a != b; got != tt.wantDistinctID {
				t.Errorf("StableID(%q,%q)=%q vs StableID(%q,%q)=%q: distinct=%v, want %v",
					tt.srcA, tt.natA, a, tt.srcB, tt.natB, b, got, tt.wantDistinctID)
			}
		})
	}
}

func TestReadState_Next(t *testing.T) {
	tests := []struct {
		name string
		in   ReadState
		want ReadState
	}{
		{"any cycles to unread-only", ReadAny, ReadUnreadOnly},
		{"unread-only cycles to read-only", ReadUnreadOnly, ReadReadOnly},
		{"read-only cycles back to any", ReadReadOnly, ReadAny},
		{"out of range falls back to any", ReadState(99), ReadAny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.Next(); got != tt.want {
				t.Errorf("ReadState(%d).Next() = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
