package converge

import (
	"bytes"
	"errors"
	"testing"
)

var threeOrders = [6][3]int{
	{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0},
}

// threeStates returns the full state of three replicas that edited one document
// concurrently. Every pair of them overlaps, and none of them holds it all.
func threeStates(t *testing.T) []Update {
	t.Helper()
	a := NewDocWith(Options{ClientID: 1})
	b := NewDocWith(Options{ClientID: 2})
	c := NewDocWith(Options{ClientID: 3})
	ta, tb, tc := a.Text("body"), b.Text("body"), c.Text("body")
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 0, "hello") })
	sendState(t, a, b)
	sendState(t, a, c)

	b.Transact(nil, func(tx *Tx) { tx.Insert(tb, 5, " world") })
	c.Transact(nil, func(tx *Tx) { tx.Delete(tc, 1, 1) })
	c.Transact(nil, func(tx *Tx) { tx.Insert(tc, 1, "a") })
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 5, "!") })
	sendState(t, c, b)

	return []Update{
		a.EncodeStateAsUpdate(StateVector{}),
		b.EncodeStateAsUpdate(StateVector{}),
		c.EncodeStateAsUpdate(StateVector{}),
	}
}

// integrateAll returns a document holding every update, delivered in order.
func integrateAll(t *testing.T, us ...Update) *Doc {
	t.Helper()
	d := NewDocWith(Options{ClientID: 9})
	for i, u := range us {
		if err := d.ApplyUpdate(u, "peer"); err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}
	return d
}

func TestMergeUpdatesIsOrderIndependent(t *testing.T) {
	us := threeStates(t)
	want, err := MergeUpdates(us...)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	for _, order := range threeOrders {
		got, err := MergeUpdates(us[order[0]], us[order[1]], us[order[2]])
		if err != nil {
			t.Fatalf("order %v: %v", order, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("order %v merges to\n%s\nwant\n%s", order, hexDump(got), hexDump(want))
		}
	}
}

func TestMergeUpdatesEqualsIntegratingEveryInput(t *testing.T) {
	us := threeStates(t)
	m, err := MergeUpdates(us...)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	d := integrateAll(t, us...)
	if got := d.EncodeStateAsUpdate(StateVector{}); !bytes.Equal(m, got) {
		t.Fatalf("the merge reads\n%s\nthe document that took all three reads\n%s",
			hexDump(m), hexDump(got))
	}
	// and the merge on its own rebuilds that document
	assertConverged(t, d, integrateAll(t, m), d.Text("body").String())
}

func TestMergeUpdatesIsIdempotent(t *testing.T) {
	us := threeStates(t)
	m, err := MergeUpdates(us...)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	for _, again := range [][]Update{{m}, {m, m}, {m, us[0]}} {
		got, err := MergeUpdates(again...)
		if err != nil {
			t.Fatalf("remerge of %d: %v", len(again), err)
		}
		if !bytes.Equal(got, m) {
			t.Fatalf("remerging %d updates changed the bytes:\n%s\n%s",
				len(again), hexDump(got), hexDump(m))
		}
	}
}

func TestMergeUpdatesJoinsARunSplitAcrossInputs(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "abc") })
	head := d.EncodeStateAsUpdate(StateVector{})
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 3, "def") })
	// the two inputs overlap at clock 2, so the merge has to slice before it folds
	tail := d.EncodeStateAsUpdate(StateVector{m: map[ClientID]uint64{1: 2}})
	whole := d.EncodeStateAsUpdate(StateVector{})

	for _, in := range [][]Update{{head, tail}, {tail, head}} {
		got, err := MergeUpdates(in...)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if !bytes.Equal(got, whole) {
			t.Fatalf("the halves merge to\n%s\nwant the one run\n%s", hexDump(got), hexDump(whole))
		}
	}
	if n := structCount(t, whole); n != 1 {
		t.Fatalf("the document encodes %d structs, so the test proves nothing", n)
	}
}

func TestMergeUpdatesOfNothingIsAValidNoOp(t *testing.T) {
	empty, err := MergeUpdates()
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	got, err := MergeUpdates(nil, Update{}, empty)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !bytes.Equal(got, empty) {
		t.Fatalf("merging nothing gave %s, want %s", hexDump(got), hexDump(empty))
	}
	d := NewDocWith(Options{ClientID: 1})
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "x") })
	before := d.EncodeStateAsUpdate(StateVector{})
	if err := d.ApplyUpdate(empty, "peer"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if after := d.EncodeStateAsUpdate(StateVector{}); !bytes.Equal(before, after) {
		t.Fatal("an empty update changed the document")
	}
}

func TestUpdateStateVectorReportsWhatIsIntegrable(t *testing.T) {
	us := threeStates(t)
	d := integrateAll(t, us...)
	whole := d.EncodeStateAsUpdate(StateVector{})

	sv, err := whole.StateVector()
	if err != nil {
		t.Fatalf("state vector: %v", err)
	}
	want, _ := d.StateVector().MarshalBinary()
	got, _ := sv.MarshalBinary()
	if !bytes.Equal(got, want) {
		t.Fatalf("the update reports %s, the document %s", sv, d.StateVector())
	}
}

func TestUpdateStateVectorStopsAtAGap(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "abcdef") })
	tail, err := d.EncodeStateAsUpdate(StateVector{}).Diff(StateVector{m: map[ClientID]uint64{1: 2}})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	sv, err := tail.StateVector()
	if err != nil {
		t.Fatalf("state vector: %v", err)
	}
	// clocks 0 and 1 are missing, so nothing in it is integrable on its own
	if got := sv.Get(1); got != 0 {
		t.Fatalf("an update starting at clock 2 reports %d", got)
	}
}

func TestDiffKeepsTheWholeDeleteSet(t *testing.T) {
	a := NewDocWith(Options{ClientID: 1})
	b := NewDocWith(Options{ClientID: 2})
	ta := a.Text("body")
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 0, "hello") })
	sendState(t, a, b)
	a.Transact(nil, func(tx *Tx) { tx.Delete(ta, 1, 1) })

	// b holds every clock a holds, so the diff carries the deletion and nothing else
	d, err := a.EncodeStateAsUpdate(StateVector{}).Diff(b.StateVector())
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	structs, ds, err := decodeUpdate(d)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(structs) != 0 {
		t.Fatalf("the diff carries %d clients of structs, want none", len(structs))
	}
	if ds.empty() {
		t.Fatal("the diff dropped the delete set, which resurrects the rune on the receiver")
	}
	if err := b.ApplyUpdate(d, "peer"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	assertConverged(t, a, b, "hllo")
}

func TestDiffReachesTheSameStateAsTheWholeUpdate(t *testing.T) {
	us := threeStates(t)
	whole := integrateAll(t, us...).EncodeStateAsUpdate(StateVector{})

	full := integrateAll(t, us[0], whole)
	part := integrateAll(t, us[0])
	d, err := whole.Diff(part.StateVector())
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if err := part.ApplyUpdate(d, "peer"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	assertConverged(t, full, part, full.Text("body").String())
}

func TestUpdateAlgebraRejectsMalformedBytes(t *testing.T) {
	bad := Update{blobMagic, kindUpdate, blobVersion, 0x7f}
	if _, err := MergeUpdates(bad); !errors.Is(err, ErrMalformedUpdate) {
		t.Fatalf("MergeUpdates: %v", err)
	}
	if _, err := bad.Diff(StateVector{}); !errors.Is(err, ErrMalformedUpdate) {
		t.Fatalf("Diff: %v", err)
	}
	if _, err := bad.StateVector(); !errors.Is(err, ErrMalformedUpdate) {
		t.Fatalf("StateVector: %v", err)
	}
}

// structCount reports how many structs u carries, so a test can prove the runs
// it merged really did fold.
func structCount(t *testing.T, u Update) int {
	t.Helper()
	structs, _, err := decodeUpdate(u)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	n := 0
	for _, ss := range structs {
		n += len(ss)
	}
	return n
}

func TestMergeUpdatesRejectsACyclicUnion(t *testing.T) {
	// each input names a struct only the other one carries, so the cycle is in
	// neither of them and in both together
	first := encodeStructs(map[ClientID][]decoded{
		1: {{client: 1, origin: ID{Client: 2}, content: "a", runeLen: 1, u16Len: 1}},
	}, deleteSet{})
	second := encodeStructs(map[ClientID][]decoded{
		2: {{client: 2, origin: ID{Client: 1}, content: "b", runeLen: 1, u16Len: 1}},
	}, deleteSet{})
	for i, u := range []Update{first, second} {
		if _, err := u.StateVector(); err != nil {
			t.Fatalf("input %d is already invalid on its own: %v", i, err)
		}
	}
	if _, err := MergeUpdates(first, second); !errors.Is(err, ErrMalformedUpdate) {
		t.Fatalf("the merge accepted a cyclic union: %v", err)
	}
}
