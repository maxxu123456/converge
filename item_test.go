package converge

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

// Written as escapes so the file stays ASCII and the expected widths are visible.
var splitSamples = []string{
	"hello world",
	"\u65e5\u672c\u8a9e\u30c6\u30ad\u30b9\u30c8", // CJK and kana, three bytes each
	"e\u0301a\u0300o\u0308",                      // letters plus combining marks
	"\U0001F600\U0001F389\U0001F9EC",             // astral emoji, two UTF-16 units each
	"a\U0001F600b\u65e5c\u0301d",                 // mixed widths
}

func newRun(c ClientID, clock uint64, content string) *item {
	return &item{
		id:      ID{c, clock},
		content: content,
		runeLen: uint32(utf8.RuneCountInString(content)),
		u16Len:  utf16LenOf(content),
	}
}

// chain adds the runs to s in order and links them into one list, giving each
// the neighbours it would have been born with.
func chain(s *structStore, parent *Text, its ...*item) {
	for i, it := range its {
		it.parent = parent
		if i > 0 {
			it.left = its[i-1]
			its[i-1].right = it
			it.origin = its[i-1].lastID()
		}
		if i+1 < len(its) {
			it.rightOrigin = its[i+1].id
		}
		s.add(it)
	}
}

func checkContiguous(t *testing.T, s *structStore) {
	t.Helper()
	for c, cb := range s.clients {
		want := uint64(0)
		for i, it := range cb.blocks {
			if it.runeLen == 0 {
				t.Fatalf("client %v block %d is empty", c, i)
			}
			if it.id.Clock != want {
				t.Fatalf("client %v block %d starts at %d, want %d", c, i, it.id.Clock, want)
			}
			want = it.endClock()
		}
		if cb.next != want {
			t.Fatalf("client %v next is %d, want %d", c, cb.next, want)
		}
	}
}

// blockText joins one client's blocks in clock order.
func blockText(s *structStore, c ClientID) string {
	var b strings.Builder
	for _, it := range s.clients[c].blocks {
		b.WriteString(it.content)
	}
	return b.String()
}

// listText walks the list from head, tombstones included.
func listText(head *item) string {
	var b strings.Builder
	for it := head; it != nil; it = it.right {
		b.WriteString(it.content)
	}
	return b.String()
}

func TestSplitAtEveryOffset(t *testing.T) {
	for si, sample := range splitSamples {
		runes := []rune(sample)
		for off := 1; off < len(runes); off++ {
			s := &structStore{}
			parent := &Text{name: "body"}
			left := newRun(1, 0, "L")
			mid := newRun(2, 0, sample)
			right := newRun(3, 0, "R")
			chain(s, parent, left, mid, right)
			mid.deleted = off%2 == 0
			wantOrigin, wantRightOrigin := mid.origin, mid.rightOrigin
			wantU16 := mid.u16Len

			r := s.splitAt(mid, uint32(off))

			what := func(format string, args ...any) {
				t.Helper()
				t.Fatalf("sample %d offset %d: "+format, append([]any{si, off}, args...)...)
			}
			if got, want := mid.content, string(runes[:off]); got != want {
				what("left content %q, want %q", got, want)
			}
			if got, want := r.content, string(runes[off:]); got != want {
				what("right content %q, want %q", got, want)
			}
			if mid.runeLen != uint32(off) || r.runeLen != uint32(len(runes)-off) {
				what("rune lengths %d and %d, want %d and %d",
					mid.runeLen, r.runeLen, off, len(runes)-off)
			}
			if mid.u16Len != utf16LenOf(mid.content) || r.u16Len != utf16LenOf(r.content) {
				what("u16 lengths %d and %d do not match the halves' content",
					mid.u16Len, r.u16Len)
			}
			if mid.u16Len+r.u16Len != wantU16 {
				what("u16 lengths sum to %d, want %d", mid.u16Len+r.u16Len, wantU16)
			}
			if want := (ID{2, uint64(off)}); r.id != want {
				what("right id %v, want %v", r.id, want)
			}
			if r.origin != mid.lastID() {
				what("right origin %v, want the left half's last id %v", r.origin, mid.lastID())
			}
			if r.rightOrigin != wantRightOrigin {
				what("right rightOrigin %v, want the inherited %v", r.rightOrigin, wantRightOrigin)
			}
			if mid.origin != wantOrigin {
				what("left origin changed to %v, want %v", mid.origin, wantOrigin)
			}
			if r.deleted != mid.deleted || r.parent != mid.parent {
				what("right half did not inherit deleted or parent")
			}
			if mid.right != r || r.left != mid {
				what("halves are not linked to each other")
			}
			if r.right != right || right.left != r {
				what("right neighbour is not linked to the right half")
			}
			if mid.left != left || left.right != mid {
				what("left neighbour lost its link")
			}
			if s.get(ID{2, uint64(off)}) != r || s.get(ID{2, uint64(off) - 1}) != mid {
				what("store does not return the halves for their own clocks")
			}
			if n := itemCount(s); n != 4 {
				what("store holds %d blocks, want 4", n)
			}
			if got := s.stateOf(2); got != uint64(len(runes)) {
				what("a split changed stateOf to %d, want %d", got, len(runes))
			}
			checkContiguous(t, s)
			if got := listText(left); got != "L"+sample+"R" {
				what("list reads %q after the split", got)
			}
		}
	}
}

func TestSplitAtOutsideTheRunPanics(t *testing.T) {
	for _, off := range []uint32{0, 5, 6} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("splitAt at offset %d did not panic", off)
				}
			}()
			s := &structStore{}
			it := newRun(1, 0, "hello")
			chain(s, &Text{}, it)
			s.splitAt(it, off)
		}()
	}
}

func TestSplitAtAnItemOutsideTheStorePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("splitAt of an unstored item did not panic")
		}
	}()
	s := &structStore{}
	s.splitAt(newRun(1, 0, "hello"), 2)
}

func TestGetReturnsTheContainingBlock(t *testing.T) {
	s := &structStore{}
	a := newRun(9, 0, "abc")
	b := newRun(9, 3, "de")
	c := newRun(9, 5, "fghi")
	chain(s, &Text{}, a, b, c)

	want := []*item{a, a, a, b, b, c, c, c, c}
	for clock, it := range want {
		if got := s.get(ID{9, uint64(clock)}); got != it {
			t.Fatalf("get(clock %d) = %v, want the block starting at %d",
				clock, got, it.id.Clock)
		}
	}
	for _, clock := range []uint64{9, 10, 1 << 40} {
		if got := s.get(ID{9, clock}); got != nil {
			t.Fatalf("get(clock %d) = %v, want nil past next", clock, got)
		}
	}
	if got := s.get(ID{8, 0}); got != nil {
		t.Fatalf("get for an unknown client = %v, want nil", got)
	}
	if _, ok := s.clients[9].find(9); ok {
		t.Fatal("find reported a block at next")
	}
}

func TestNextBlock(t *testing.T) {
	s := &structStore{}
	a := newRun(9, 0, "abc")
	b := newRun(9, 3, "de")
	chain(s, &Text{}, a, b)

	if got := s.nextBlock(a); got != b {
		t.Fatalf("nextBlock(a) = %v, want b", got)
	}
	if got := s.nextBlock(b); got != nil {
		t.Fatalf("nextBlock(b) = %v, want nil at the end", got)
	}
	r := s.splitAt(b, 1)
	if got := s.nextBlock(b); got != r {
		t.Fatalf("nextBlock(b) = %v, want the new right half", got)
	}
	if got := s.nextBlock(newRun(7, 0, "x")); got != nil {
		t.Fatalf("nextBlock of an unknown client = %v, want nil", got)
	}
}

func TestAddRejectsAGapAndAnOverlap(t *testing.T) {
	for _, clock := range []uint64{1, 3} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("add at clock %d did not panic", clock)
				}
			}()
			s := &structStore{}
			s.add(newRun(4, 0, "ab"))
			s.add(newRun(4, clock, "c"))
		}()
	}
}

func TestStateOfAndStateVector(t *testing.T) {
	s := &structStore{}
	if got := s.stateOf(1); got != 0 {
		t.Fatalf("stateOf on an empty store = %d, want 0", got)
	}
	parent := &Text{}
	chain(s, parent, newRun(1, 0, "abc"), newRun(2, 0, "de"))
	s.add(newRun(1, 3, "fg"))

	if got := s.stateOf(1); got != 5 {
		t.Fatalf("stateOf(1) = %d, want 5", got)
	}
	if got := s.stateOf(3); got != 0 {
		t.Fatalf("stateOf for an unknown client = %d, want 0", got)
	}
	before := s.stateVector()
	if len(before) != 2 || before[1] != 5 || before[2] != 2 {
		t.Fatalf("stateVector = %v, want {1:5, 2:2}", before)
	}
	forceSplit(s)
	after := s.stateVector()
	if len(after) != len(before) || after[1] != before[1] || after[2] != before[2] {
		t.Fatalf("splitting changed the state vector: %v then %v", before, after)
	}
}

func TestCleanStartAndCleanEnd(t *testing.T) {
	const content = "abcdefgh"
	for clock := uint64(0); clock < uint64(len(content)); clock++ {
		s := &structStore{}
		it := newRun(5, 0, content)
		chain(s, &Text{}, it, newRun(6, 0, "Z"))

		start := s.cleanStart(ID{5, clock})
		if start.id.Clock != clock {
			t.Fatalf("cleanStart(%d) starts at %d", clock, start.id.Clock)
		}
		n := itemCount(s)
		if again := s.cleanStart(ID{5, clock}); again != start || itemCount(s) != n {
			t.Fatalf("cleanStart(%d) split a block that already started there", clock)
		}
		end := s.cleanEnd(ID{5, clock})
		if end.endClock() != clock+1 {
			t.Fatalf("cleanEnd(%d) ends at %d, want %d", clock, end.endClock(), clock+1)
		}
		n = itemCount(s)
		if again := s.cleanEnd(ID{5, clock}); again != end || itemCount(s) != n {
			t.Fatalf("cleanEnd(%d) split a block that already ended there", clock)
		}
		checkContiguous(t, s)
		if got := blockText(s, 5); got != content {
			t.Fatalf("blocks read %q after cleaning at %d, want %q", got, clock, content)
		}
		if got := listText(s.clients[5].blocks[0]); got != content+"Z" {
			t.Fatalf("list reads %q after cleaning at %d", got, clock)
		}
	}

	s := &structStore{}
	chain(s, &Text{}, newRun(5, 0, "ab"))
	if got := s.cleanStart(ID{5, 2}); got != nil {
		t.Fatalf("cleanStart past next = %v, want nil", got)
	}
	if got := s.cleanEnd(ID{7, 0}); got != nil {
		t.Fatalf("cleanEnd for an unknown client = %v, want nil", got)
	}
}

func TestForceSplitShattersEveryRun(t *testing.T) {
	s := &structStore{}
	parent := &Text{}
	head := newRun(1, 0, splitSamples[3])
	chain(s, parent, head, newRun(2, 0, "plain"), newRun(3, 0, splitSamples[1]))
	want := listText(head)
	runes := 0
	for _, cb := range s.clients {
		runes += int(cb.next)
	}

	forceSplit(s)

	if got := itemCount(s); got != runes {
		t.Fatalf("store holds %d blocks after shattering, want one per rune (%d)", got, runes)
	}
	for c, cb := range s.clients {
		for _, it := range cb.blocks {
			if it.runeLen != 1 || it.u16Len != utf16LenOf(it.content) {
				t.Fatalf("client %v block %v has runeLen %d and u16Len %d",
					c, it.id, it.runeLen, it.u16Len)
			}
		}
	}
	checkContiguous(t, s)
	if got := listText(head); got != want {
		t.Fatalf("list reads %q after shattering, want %q", got, want)
	}
	for it := head; it.right != nil; it = it.right {
		if it.right.left != it {
			t.Fatalf("block %v is not linked back from its successor", it.id)
		}
	}
}

func TestUTF8ByteOffsetAndUTF16Len(t *testing.T) {
	for _, sample := range splitSamples {
		runes := []rune(sample)
		for off := 0; off <= len(runes); off++ {
			got := utf8ByteOffset(sample, uint32(off))
			if want := len(string(runes[:off])); got != want {
				t.Fatalf("utf8ByteOffset(%q, %d) = %d, want %d", sample, off, got, want)
			}
		}
		if got, want := utf16LenOf(sample), uint32(len(utf16.Encode(runes))); got != want {
			t.Fatalf("utf16LenOf(%q) = %d, want %d", sample, got, want)
		}
	}
}

// mergePair returns a store holding two contiguous runs of one client, linked
// the way a split leaves them, with a neighbour on each side.
func mergePair() (*structStore, *item, *item) {
	s := &structStore{}
	parent := &Text{name: "body"}
	l, r := newRun(2, 0, "ab"), newRun(2, 2, "cd")
	chain(s, parent, newRun(1, 0, "L"), l, r, newRun(3, 0, "R"))
	l.rightOrigin = r.rightOrigin // both halves of a split name the same right neighbour
	return s, l, r
}

func TestTryMergeLeftFoldsOneRun(t *testing.T) {
	s, l, r := mergePair()
	tail := r.right

	if !tryMergeLeft(s, r) {
		t.Fatal("two halves of one run did not fold")
	}
	if l.content != "abcd" || l.runeLen != 4 || l.u16Len != 4 {
		t.Fatalf("left run holds %q, %d runes, %d units", l.content, l.runeLen, l.u16Len)
	}
	if l.endClock() != 4 {
		t.Fatalf("the folded run ends at clock %d, want 4", l.endClock())
	}
	if l.right != tail || tail.left != l {
		t.Fatal("the folded run is not linked to what followed it")
	}
	if got := listText(s.clients[1].blocks[0]); got != "LabcdR" {
		t.Fatalf("the list reads %q after the fold", got)
	}
}

func TestSplitThenFoldRestoresTheRun(t *testing.T) {
	for si, sample := range splitSamples {
		for off := 1; off < len([]rune(sample)); off++ {
			s := &structStore{}
			it := newRun(2, 0, sample)
			chain(s, &Text{name: "body"}, newRun(1, 0, "L"), it, newRun(3, 0, "R"))
			it.deleted = off%2 == 0
			want := *it

			if !tryMergeLeft(s, s.splitAt(it, uint32(off))) {
				t.Fatalf("sample %d offset %d: the halves of one split did not fold", si, off)
			}
			if *it != want {
				t.Fatalf("sample %d offset %d: folding back gave %+v, want %+v", si, off, *it, want)
			}
		}
	}
}

func TestTryMergeLeftRequiresEveryPrecondition(t *testing.T) {
	cases := []struct {
		name  string
		spoil func(l, r *item)
	}{
		{"no left neighbour", func(l, r *item) { r.left = nil }},
		{"another root", func(l, r *item) { r.parent = &Text{name: "notes"} }},
		{"another client", func(l, r *item) { r.id.Client = 7 }},
		{"a clock gap", func(l, r *item) { r.id.Clock++ }},
		{"something between them", func(l, r *item) { l.right = newRun(4, 0, "M") }},
		{"an origin naming elsewhere", func(l, r *item) { r.origin = ID{1, 0} }},
		{"a narrower insertion interval", func(l, r *item) { r.rightOrigin = ID{9, 0} }},
		{"one side tombstoned", func(l, r *item) { r.deleted = true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, l, r := mergePair()
			c.spoil(l, r)
			if tryMergeLeft(s, r) {
				t.Fatal("folded a pair that no split could have produced")
			}
			if l.content != "ab" || l.runeLen != 2 || l.endClock() != 2 {
				t.Fatalf("a refused fold left %q, %d runes", l.content, l.runeLen)
			}
		})
	}
}

func TestTypingBurstFoldsIntoOneRun(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	tb := d.Text("body")
	for _, r := range "hello" {
		d.Transact(nil, func(tx *Tx) { tx.Insert(tb, tx.Len(tb), string(r)) })
		validate(t, d)
	}
	if got := tb.String(); got != "hello" {
		t.Fatalf("text is %q, want %q", got, "hello")
	}
	if got := itemCount(&d.store); got != 1 {
		t.Fatalf("five keystrokes left %d runs, want one", got)
	}
}

func TestTombstonesFoldWhenTheirNeighbourGoes(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "abcd") })
	d.Transact(nil, func(tx *Tx) { tx.Delete(tb, 2, 2) })
	d.Transact(nil, func(tx *Tx) { tx.Delete(tb, 0, 2) })
	validate(t, d)

	if got := tb.String(); got != "" {
		t.Fatalf("text is %q, want it all deleted", got)
	}
	if got := itemCount(&d.store); got != 1 {
		t.Fatalf("the store holds %d runs, want the tombstones folded into one", got)
	}
}

func TestDeltaAfterMergeEmitsInsert(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "hello") })

	var got []Delta
	tb.Observe(func(ev Event) { got = append(got, ev.Delta...) })
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 5, "!") })

	want := []Delta{{Retain: 5}, {Insert: "!"}}
	if !slices.Equal(got, want) {
		t.Fatalf("delta %v, want %v", got, want)
	}
	if n := itemCount(&d.store); n != 1 {
		t.Fatalf("the store holds %d runs, so the keystroke was classified before the fold", n)
	}
}

// TestMergeRequiresRightOriginEquality builds the one shape where folding
// without the rightOrigin test diverges: replica a holds l and r list-adjacent
// and folds them, then w arrives and splits the fold back, inheriting the wrong
// right neighbour. Replica c never sees them adjacent, so it keeps r's own.
func TestMergeRequiresRightOriginEquality(t *testing.T) {
	a := NewDocWith(Options{ClientID: 5})
	b := NewDocWith(Options{ClientID: 9})
	c := NewDocWith(Options{ClientID: 3})
	ta, tc := a.Text("body"), c.Text("body")
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 0, "l") })
	b.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("body"), 0, "z") })
	sendState(t, a, b)
	sendState(t, b, a)
	sendState(t, a, c)

	// a types between l and z, so its two runs name different right neighbours.
	// The update goes out before the fold, so c holds the run a folded away.
	var typed Update
	cancel := a.OnUpdate(func(u Update, _ any) { typed = u })
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 1, "r") })
	cancel()

	// c types at the same spot and wins the tiebreak, so on c the two runs of a
	// are never list-adjacent
	c.Transact(nil, func(tx *Tx) { tx.Insert(tc, 1, "w") })
	if err := c.ApplyUpdate(typed, "peer"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	sendState(t, c, a)
	sendState(t, a, b)
	sendState(t, c, b)
	validate(t, a)
	validate(t, b)
	validate(t, c)
	assertConverged(t, a, c, "lwrz")
	assertConverged(t, a, b, "lwrz")
}

func TestMergeRemovesFoldedItemFromStore(t *testing.T) {
	s, l, r := mergePair()
	if !tryMergeLeft(s, r) {
		t.Fatal("two halves of one run did not fold")
	}
	if got := s.get(r.id); got != l {
		t.Fatalf("clock %d answers with %v, want the run that absorbed it", r.id.Clock, got)
	}
	if n := itemCount(s); n != 3 {
		t.Fatalf("the store holds %d blocks, want 3", n)
	}
	checkContiguous(t, s)
}

func TestItemAnchoredInsideAFoldedRunSurvives(t *testing.T) {
	a := NewDocWith(Options{ClientID: 1})
	b := NewDocWith(Options{ClientID: 2})
	ta, tb := a.Text("body"), b.Text("body")
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 0, "ab") })
	// the second burst folds into the first at commit, so this clock is only
	// covered by the folded run
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 2, "cd") })
	sendState(t, a, b)

	b.Transact(nil, func(tx *Tx) { tx.Insert(tb, 3, "X") })
	sendState(t, b, a)

	validate(t, a)
	validate(t, b)
	assertConverged(t, a, b, "abcXd")
}

// markerDoc returns a document holding "abcdef" as three runs, each typed in
// front of the last so the merge pass leaves them apart.
func markerDoc(t *testing.T) (*Doc, *Text) {
	t.Helper()
	d := NewDocWith(Options{ClientID: 1})
	tb := d.Text("body")
	for _, s := range []string{"ef", "cd", "ab"} {
		text := s
		d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, text) })
	}
	validate(t, d)
	return d, tb
}

// clearMarkers empties the cache, so a case starts from a known state. Only
// the tests need this: the library keeps its markers up to date instead.
func (t *Text) clearMarkers() { t.markers = [numMarkers]marker{} }

// itemsOf lists t's runs in list order, tombstones included.
func itemsOf(t *Text) []*item {
	var its []*item
	for it := t.start; it != nil; it = it.right {
		its = append(its, it)
	}
	return its
}

// wantMarkers fails unless the cache holds exactly want: one rune index per item.
func wantMarkers(t *testing.T, tb *Text, want map[*item]int) {
	t.Helper()
	got := make(map[*item]int)
	for i := range tb.markers {
		if m := &tb.markers[i]; m.stamp != 0 {
			got[m.it] = m.rune
		}
	}
	for it, r := range want {
		switch g, cached := got[it]; {
		case !cached:
			t.Fatalf("nothing cached for %q, want rune %d", it.content, r)
		case g != r:
			t.Fatalf("%q is cached at rune %d, want %d", it.content, g, r)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("the cache holds %d markers, want %d", len(got), len(want))
	}
}

// peerOf returns a second replica holding everything d holds.
func peerOf(t *testing.T, d *Doc) (*Doc, *Text) {
	t.Helper()
	e := NewDocWith(Options{ClientID: 9})
	sendState(t, d, e)
	return e, e.Text("body")
}

// TestMarkerTransitions walks every way a marker is allowed to move. Anything
// else touching those fields is silent corruption: a stale index inserts text
// in the wrong place and raises no error.
func TestMarkerTransitions(t *testing.T) {
	t.Run("a lookup marks where it stopped", func(t *testing.T) {
		d, tb := markerDoc(t)
		its := itemsOf(tb)
		tb.clearMarkers()
		if got := tb.Slice(2, 4); got != "cd" {
			t.Fatalf("Slice(2, 4) = %q", got)
		}
		wantMarkers(t, tb, map[*item]int{its[1]: 2})
		validate(t, d)
	})

	t.Run("a local insert shifts what follows and marks itself", func(t *testing.T) {
		d, tb := markerDoc(t)
		its := itemsOf(tb)
		tb.clearMarkers()
		tb.installMarker(its[2], 4, 4)
		d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 1, "X") })
		left := itemsOf(tb)[0] // the "ab" run, split by the insert
		wantMarkers(t, tb, map[*item]int{left: 0, left.right: 1, its[2]: 5})
		validate(t, d)
	})

	t.Run("a local delete drops its markers and pulls the rest back", func(t *testing.T) {
		d, tb := markerDoc(t)
		its := itemsOf(tb)
		tb.clearMarkers()
		tb.installMarker(its[1], 2, 2)
		tb.installMarker(its[2], 4, 4)
		d.Transact(nil, func(tx *Tx) { tx.Delete(tb, 2, 2) })
		wantMarkers(t, tb, map[*item]int{its[2]: 2})
		validate(t, d)
	})

	t.Run("a remote insert shifts what follows and marks itself", func(t *testing.T) {
		d, tb := markerDoc(t)
		its := itemsOf(tb)
		e, te := peerOf(t, d)
		e.Transact(nil, func(tx *Tx) { tx.Insert(te, 1, "Z") })
		tb.clearMarkers()
		tb.installMarker(its[2], 4, 4)
		sendState(t, e, d)
		wantMarkers(t, tb, map[*item]int{its[0].right: 1, its[2]: 5})
		validate(t, d)
	})

	t.Run("a remote delete drops its markers and pulls the rest back", func(t *testing.T) {
		d, tb := markerDoc(t)
		its := itemsOf(tb)
		e, te := peerOf(t, d)
		e.Transact(nil, func(tx *Tx) { tx.Delete(te, 2, 2) })
		tb.clearMarkers()
		tb.installMarker(its[1], 2, 2)
		tb.installMarker(its[2], 4, 4)
		sendState(t, e, d)
		wantMarkers(t, tb, map[*item]int{its[2]: 2})
		validate(t, d)
	})

	t.Run("a split leaves the marker on the left half", func(t *testing.T) {
		d, tb := markerDoc(t)
		its := itemsOf(tb)
		tb.clearMarkers()
		tb.installMarker(its[1], 2, 2)
		d.store.splitAt(its[1], 1)
		wantMarkers(t, tb, map[*item]int{its[1]: 2})
		validate(t, d)
	})

	t.Run("a fold repoints the marker onto the run that swallowed it", func(t *testing.T) {
		d, tb := markerDoc(t)
		its := itemsOf(tb)
		tb.clearMarkers()
		right := d.store.splitAt(its[1], 1)
		tb.installMarker(right, 3, 3)
		if !tryMergeLeft(&d.store, right) {
			t.Fatal("the halves of one split did not fold")
		}
		wantMarkers(t, tb, map[*item]int{its[1]: 2})
		validate(t, d)
	})
}
