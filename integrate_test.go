package converge

import (
	"bytes"
	"errors"
	"testing"
)

// sendState hands to everything from holds that to does not, the way a peer
// answers a state vector.
func sendState(t *testing.T, from, to *Doc) {
	t.Helper()
	if err := to.ApplyUpdate(from.EncodeStateAsUpdate(to.StateVector()), "peer"); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

// assertConverged checks both replicas agree on the text and on the bytes.
// Equal text with unequal bytes means the structure diverged underneath.
func assertConverged(t *testing.T, a, b *Doc, want string) {
	t.Helper()
	ta, tb := a.Text("body"), b.Text("body")
	if ta.String() != want {
		t.Fatalf("first replica has %q, want %q", ta.String(), want)
	}
	if tb.String() != want {
		t.Fatalf("second replica has %q, want %q", tb.String(), want)
	}
	ua := a.EncodeStateAsUpdate(StateVector{})
	ub := b.EncodeStateAsUpdate(StateVector{})
	if !bytes.Equal(ua, ub) {
		t.Fatalf("replicas encode differently:\n%s\n%s", hexDump(ua), hexDump(ub))
	}
	checkLinks(t, ta)
	checkLinks(t, tb)
}

func TestTwoDocsConvergeBothWays(t *testing.T) {
	a := NewDocWith(Options{ClientID: 1})
	b := NewDocWith(Options{ClientID: 2})
	ta, tb := a.Text("body"), b.Text("body")

	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 0, "hello world") })
	sendState(t, a, b)
	if got := tb.String(); got != "hello world" {
		t.Fatalf("after the first sync the second replica has %q", got)
	}

	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 5, ",") })
	b.Transact(nil, func(tx *Tx) {
		tx.Delete(tb, 0, 1)
		tx.Insert(tb, 0, "H")
	})
	sendState(t, a, b)
	sendState(t, b, a)
	assertConverged(t, a, b, "Hello, world")

	// a second round on top of the merged state, from both sides again
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, tx.Len(ta), "!") })
	b.Transact(nil, func(tx *Tx) { tx.Delete(tb, 5, 2) })
	sendState(t, a, b)
	sendState(t, b, a)
	assertConverged(t, a, b, "Helloworld!")
}

func TestTwoTextsConvergeIndependently(t *testing.T) {
	a := NewDocWith(Options{ClientID: 4})
	b := NewDocWith(Options{ClientID: 3})
	a.Transact(nil, func(tx *Tx) {
		tx.Insert(tx.Text("body"), 0, "one")
		tx.Insert(tx.Text("title"), 0, "two")
	})
	// the lower client id takes the head of title on both replicas
	b.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("title"), 0, "zero ") })
	sendState(t, a, b)
	sendState(t, b, a)
	assertConverged(t, a, b, "one")
	if got := a.Text("title").String(); got != "zero two" {
		t.Fatalf("title is %q on the first replica", got)
	}
	if got := b.Text("title").String(); got != "zero two" {
		t.Fatalf("title is %q on the second replica", got)
	}
}

func TestConcurrentInsertsOrderByClientID(t *testing.T) {
	a := NewDocWith(Options{ClientID: 42})
	b := NewDocWith(Options{ClientID: 7})
	a.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("body"), 0, "hi") })
	b.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("body"), 0, "X") })
	ua := a.EncodeStateAsUpdate(StateVector{})
	ub := b.EncodeStateAsUpdate(StateVector{})
	if err := a.ApplyUpdate(ub, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.ApplyUpdate(ua, nil); err != nil {
		t.Fatal(err)
	}
	// the lower client id wins the head, on both replicas
	assertConverged(t, a, b, "Xhi")
}

func TestDuplicateUpdateChangesNothingAndEmitsNothing(t *testing.T) {
	a := NewDocWith(Options{ClientID: 1})
	ta := a.Text("body")
	a.Transact(nil, func(tx *Tx) {
		tx.Insert(ta, 0, "abcd")
		tx.Delete(ta, 1, 1)
	})
	u := a.EncodeStateAsUpdate(StateVector{})

	b := NewDocWith(Options{ClientID: 2})
	emitted := 0
	b.OnUpdate(func(Update, any) { emitted++ })
	for i := 0; i < 3; i++ {
		if err := b.ApplyUpdate(u, nil); err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
		if got := b.Text("body").String(); got != "acd" {
			t.Fatalf("after delivery %d the text is %q", i, got)
		}
	}
	if emitted != 1 {
		t.Fatalf("three deliveries of one update emitted %d updates, want 1", emitted)
	}
	assertConverged(t, a, b, "acd")
}

func TestOverlappingRunsAreTrimmedNotDuplicated(t *testing.T) {
	a := NewDocWith(Options{ClientID: 1})
	ta := a.Text("body")
	var ups []Update
	a.OnUpdate(func(u Update, _ any) { ups = append(ups, bytes.Clone(u)) })
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 0, "a") })
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 1, "bc") })
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 3, "def") })

	// two peers that stopped listening at different points, so their state
	// updates repeat a prefix of the run the next one carries
	behind := NewDocWith(Options{ClientID: 2})
	if err := behind.ApplyUpdate(ups[0], nil); err != nil {
		t.Fatal(err)
	}
	ahead := NewDocWith(Options{ClientID: 3})
	for _, u := range ups[:2] {
		if err := ahead.ApplyUpdate(u, nil); err != nil {
			t.Fatal(err)
		}
	}

	d := NewDocWith(Options{ClientID: 4})
	steps := []struct {
		from *Doc
		want string
	}{
		{behind, "a"},  // [0, 1)
		{ahead, "abc"}, // [0, 3), trimmed at offset 1
		{a, "abcdef"},  // [0, 6), trimmed at offset 3
		{behind, "abcdef"},
	}
	for i, step := range steps {
		if err := d.ApplyUpdate(step.from.EncodeStateAsUpdate(StateVector{}), nil); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if got := d.Text("body").String(); got != step.want {
			t.Fatalf("step %d gives %q, want %q", i, got, step.want)
		}
	}
	assertConverged(t, a, d, "abcdef")
}

func TestRemoteDeleteSplitsTheRunItNames(t *testing.T) {
	a := NewDocWith(Options{ClientID: 1})
	b := NewDocWith(Options{ClientID: 2})
	ta := a.Text("body")
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 0, "abcdef") })
	sendState(t, a, b)
	// b holds one six-rune run and the delete names four clocks inside it
	a.Transact(nil, func(tx *Tx) { tx.Delete(ta, 1, 4) })
	sendState(t, a, b)
	assertConverged(t, a, b, "af")
}

func TestRelayedUpdatesConvergeAndStop(t *testing.T) {
	a := NewDocWith(Options{ClientID: 1})
	b := NewDocWith(Options{ClientID: 2})
	ta, tb := a.Text("body"), b.Text("body")
	delivered := 0
	a.OnUpdate(func(u Update, origin any) {
		if origin == "relay" {
			return
		}
		delivered++
		if err := b.ApplyUpdate(u, "relay"); err != nil {
			t.Errorf("relay to the second replica: %v", err)
		}
	})
	b.OnUpdate(func(u Update, origin any) {
		if origin == "relay" {
			return
		}
		delivered++
		if err := a.ApplyUpdate(u, "relay"); err != nil {
			t.Errorf("relay to the first replica: %v", err)
		}
	})

	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 0, "one ") })
	b.Transact(nil, func(tx *Tx) { tx.Insert(tb, tx.Len(tb), "two") })
	a.Transact(nil, func(tx *Tx) { tx.Delete(ta, 0, 1) })
	if delivered != 3 {
		t.Fatalf("three transactions relayed %d updates", delivered)
	}
	assertConverged(t, a, b, "ne two")
}

func TestStructWithReversedAnchorsIsRejected(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	td := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(td, 0, "ab") })
	before := d.EncodeStateAsUpdate(StateVector{})

	// origin names the second rune and rightOrigin the first, an order no
	// correct replica can produce
	u := encodeStructs(map[ClientID][]decoded{
		3: {{
			client:      3,
			origin:      ID{Client: 1, Clock: 1},
			rightOrigin: ID{Client: 1, Clock: 0},
			content:     "z",
			runeLen:     1,
			u16Len:      1,
		}},
	}, deleteSet{})

	err := d.ApplyUpdate(u, nil)
	var de *DecodeError
	if !errors.As(err, &de) || de.Field != "struct.originOrder" {
		t.Fatalf("ApplyUpdate returned %v, want a struct.originOrder DecodeError", err)
	}
	if got := td.String(); got != "ab" {
		t.Fatalf("the rejected struct left the text as %q", got)
	}
	if !bytes.Equal(before, d.EncodeStateAsUpdate(StateVector{})) {
		t.Error("the rejected struct changed the encoded state")
	}
	checkLinks(t, td)
}
