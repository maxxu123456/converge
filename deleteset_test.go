package converge

import (
	"reflect"
	"testing"
)

func TestDeleteSetAddExtendsTheLastRange(t *testing.T) {
	ds := deleteSet{}
	ds.add(1, 0, 2)
	ds.add(1, 2, 3)
	ds.add(1, 9, 1)
	ds.add(1, 0, 0)
	want := []idRange{{0, 5}, {9, 1}}
	if got := ds[1]; !reflect.DeepEqual(got, want) {
		t.Errorf("ranges %v, want %v", got, want)
	}
	if len(ds) != 1 {
		t.Errorf("an empty range created client %v", ds)
	}
}

func TestDeleteSetNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   []idRange
		want []idRange
	}{
		{"sorts", []idRange{{5, 1}, {0, 1}}, []idRange{{0, 1}, {5, 1}}},
		{"coalesces adjacent", []idRange{{0, 3}, {3, 2}}, []idRange{{0, 5}}},
		{"coalesces overlapping", []idRange{{0, 4}, {2, 5}}, []idRange{{0, 7}}},
		{"swallows a contained range", []idRange{{0, 10}, {3, 2}}, []idRange{{0, 10}}},
		{"keeps a gap", []idRange{{0, 2}, {3, 1}}, []idRange{{0, 2}, {3, 1}}},
		{"chains out of order", []idRange{{4, 1}, {0, 2}, {2, 2}, {9, 1}}, []idRange{{0, 5}, {9, 1}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ds := deleteSet{3: append([]idRange(nil), c.in...)}
			ds.normalize()
			if got := ds[3]; !reflect.DeepEqual(got, c.want) {
				t.Errorf("normalize(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestNormalizeDropsClientsWithNoRanges(t *testing.T) {
	ds := deleteSet{1: nil, 2: {{0, 1}}}
	ds.normalize()
	if _, ok := ds[1]; ok {
		t.Error("a client with no ranges survived normalize")
	}
	if ds.empty() {
		t.Error("a set holding one range reports itself empty")
	}
}

func TestDeleteSetCovers(t *testing.T) {
	ds := deleteSet{1: {{4, 3}, {9, 1}}, 2: {{0, 2}, {3, 1}}}
	cases := []struct {
		id   ID
		n    uint32
		want bool
	}{
		{ID{1, 4}, 3, true},
		{ID{1, 5}, 1, true},
		{ID{1, 6}, 1, true},
		{ID{1, 3}, 1, false},
		{ID{1, 4}, 4, false},
		{ID{1, 7}, 1, false},
		// two ranges that meet in the middle do not cover a run that spans them
		{ID{2, 0}, 4, false},
		{ID{3, 0}, 1, false},
	}
	for _, c := range cases {
		if got := ds.covers(c.id, c.n); got != c.want {
			t.Errorf("covers(%v, %d) = %v, want %v", c.id, c.n, got, c.want)
		}
	}
}

func TestDeleteSetUnion(t *testing.T) {
	a := deleteSet{1: {{0, 2}}, 2: {{5, 1}}}
	b := deleteSet{1: {{2, 2}, {8, 1}}, 3: {{0, 1}}}
	a.union(b)
	want := deleteSet{1: {{0, 4}, {8, 1}}, 2: {{5, 1}}, 3: {{0, 1}}}
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("union = %v, want %v", a, want)
	}
	// the union must copy, not alias: growing a client must not reach into b
	a.add(3, 1, 1)
	a.normalize()
	if got, want := b[3], []idRange{{0, 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("the other set changed to %v, want %v", got, want)
	}
}

func TestEmptySetIsEmpty(t *testing.T) {
	ds := deleteSet{}
	if !ds.empty() {
		t.Error("a fresh set is not empty")
	}
	ds[7] = nil
	if !ds.empty() {
		t.Error("a client with no ranges makes the set non-empty")
	}
	ds.add(7, 0, 1)
	if ds.empty() {
		t.Error("a set holding one range reports itself empty")
	}
}

func TestDeleteRecordsOneRangePerTransaction(t *testing.T) {
	d := NewDocWith(Options{ClientID: 4})
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "abcdef") })
	d.Transact(nil, func(tx *Tx) {
		tx.Delete(tb, 1, 2)
		tx.Delete(tb, 1, 1)
	})
	// clocks 1 and 2 went first, then clock 3, and they coalesce
	want := []idRange{{1, 3}}
	if got := d.tx.deleted[4]; !reflect.DeepEqual(got, want) {
		t.Errorf("transitions %v, want %v", got, want)
	}
	if got := tb.visible(); got != "aef" {
		t.Errorf("text %q, want %q", got, "aef")
	}
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "z") })
	if !d.tx.deleted.empty() {
		t.Errorf("an insert-only transaction recorded %v", d.tx.deleted)
	}
}

// deleteOnly returns a's delete set as an update carrying no structs, the shape
// a peer already caught up on structs receives.
func deleteOnly(a *Doc, c ClientID, from uint64) Update {
	return a.EncodeStateAsUpdate(StateVector{m: map[ClientID]uint64{c: from}})
}

func TestDeleteRangeAppliesWhatItCanAndBuffersTheTail(t *testing.T) {
	a := NewDocWith(Options{ClientID: 1})
	ta := a.Text("body")
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 0, "abc") })

	b := NewDocWith(Options{ClientID: 2})
	sendState(t, a, b)

	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 3, "def") })
	a.Transact(nil, func(tx *Tx) { tx.Delete(ta, 1, 4) })
	// clocks 1 to 4, of which the receiver holds 1 and 2
	if err := b.ApplyUpdate(deleteOnly(a, 1, 6), "peer"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	validate(t, b)
	if got := b.Text("body").String(); got != "a" {
		t.Fatalf("the deletable prefix left %q, want %q", got, "a")
	}
	want := []idRange{{3, 2}}
	if got := b.pendingDS[1]; !reflect.DeepEqual(got, want) {
		t.Fatalf("buffered %v, want %v", got, want)
	}
}
