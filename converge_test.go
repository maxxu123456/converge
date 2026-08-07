package converge

import "testing"

func TestNewClientIDNeverZero(t *testing.T) {
	const draws = 10000
	seen := make(map[ClientID]struct{}, draws)
	for i := 0; i < draws; i++ {
		c := newClientID()
		if c == 0 {
			t.Fatalf("draw %d returned the reserved zero id", i)
		}
		if _, dup := seen[c]; dup {
			t.Fatalf("draw %d repeated id %s", i, c)
		}
		seen[c] = struct{}{}
	}
}

func TestIDZeroValue(t *testing.T) {
	var a, b ID
	if !a.IsZero() {
		t.Error("the zero ID does not report itself absent")
	}
	if a != b {
		t.Error("two absent origins do not compare equal")
	}
	// clock must not leak into the absence test: only the client half is reserved
	if !(ID{Clock: 7}).IsZero() {
		t.Error("a zero client with a non-zero clock reports itself present")
	}
	if (ID{Client: 1}).IsZero() {
		t.Error("a real id reports itself absent")
	}
}

func TestIDString(t *testing.T) {
	got := ID{Client: 0xbeef, Clock: 12}.String()
	if got != "beef:12" {
		t.Errorf("String() = %q, want %q", got, "beef:12")
	}
}
