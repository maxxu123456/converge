package converge

import (
	"strings"
	"testing"
)

// validate fails the test unless every structural invariant holds. Property
// tests call it after every operation, not only at the end.
func validate(t testing.TB, d *Doc) {
	t.Helper()
	if err := d.checkInvariants(); err != nil {
		t.Fatalf("invariant broken: %v", err)
	}
}

// wantBroken asserts a deliberately damaged document is caught, by a message
// that names the damage.
func wantBroken(t *testing.T, d *Doc, want string) {
	t.Helper()
	err := d.checkInvariants()
	if err == nil {
		t.Fatal("the damaged document passed the invariant check")
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("the check reported %q, which does not mention %q", err, want)
	}
}

// loadedDoc returns a document holding three items: a tombstone, a live run and
// a second run appended after it.
func loadedDoc(t *testing.T) (*Doc, *Text) {
	t.Helper()
	d := NewDocWith(Options{ClientID: 1})
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "hello") })
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 5, " world") })
	d.Transact(nil, func(tx *Tx) { tx.Delete(tb, 0, 1) })
	validate(t, d)
	return d, tb
}

func TestInvariantsHoldThroughEditsAndSync(t *testing.T) {
	a := NewDocWith(Options{ClientID: 2})
	b := NewDocWith(Options{ClientID: 3})
	ta, tb := a.Text("body"), b.Text("body")
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 0, "hello") })
	validate(t, a)
	sendState(t, a, b)
	validate(t, b)

	b.Transact(nil, func(tx *Tx) { tx.Insert(tb, 5, " there") })
	a.Transact(nil, func(tx *Tx) { tx.Delete(ta, 1, 3) })
	a.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("title"), 0, "a second root") })
	validate(t, a)
	validate(t, b)
	sendState(t, a, b)
	sendState(t, b, a)
	validate(t, a)
	validate(t, b)

	// the split history must not matter either
	forceSplit(&a.store)
	validate(t, a)
}

func TestBlockGapIsCaught(t *testing.T) {
	d, _ := loadedDoc(t)
	d.store.clients[1].next += 2
	wantBroken(t, d, "next says")
}

func TestRuneCountMismatchIsCaught(t *testing.T) {
	d, _ := loadedDoc(t)
	d.store.get(ID{Client: 1, Clock: 1}).content += "!"
	wantBroken(t, d, "runeLen says")
}

func TestBrokenBackLinkIsCaught(t *testing.T) {
	d, _ := loadedDoc(t)
	d.store.get(ID{Client: 1, Clock: 5}).left = nil
	wantBroken(t, d, "does not point back")
}

func TestOrphanedItemIsCaught(t *testing.T) {
	d, tb := loadedDoc(t)
	it := d.store.get(ID{Client: 1, Clock: 1})
	it.left.right, it.right.left = it.right, it.left
	// the counters have to follow, or they are what the check reports
	tb.runeLen -= int(it.runeLen)
	tb.byteLen -= len(it.content)
	tb.u16Len -= int(it.u16Len)
	wantBroken(t, d, "not in root")
}

func TestVisibleCountersAreChecked(t *testing.T) {
	d, tb := loadedDoc(t)
	tb.runeLen++
	wantBroken(t, d, "its list holds")
}

func TestAnchorThisReplicaDoesNotHoldIsCaught(t *testing.T) {
	d, _ := loadedDoc(t)
	d.store.get(ID{Client: 1, Clock: 5}).origin = ID{Client: 9, Clock: 3}
	wantBroken(t, d, "does not hold")
}

func TestOriginOnTheWrongSideIsCaught(t *testing.T) {
	d, _ := loadedDoc(t)
	d.store.get(ID{Client: 1, Clock: 1}).origin = ID{Client: 1, Clock: 5}
	wantBroken(t, d, "at or to its right")
}

func TestRightOriginOnTheWrongSideIsCaught(t *testing.T) {
	d, _ := loadedDoc(t)
	d.store.get(ID{Client: 1, Clock: 5}).rightOrigin = ID{Client: 1, Clock: 0}
	wantBroken(t, d, "at or to its left")
}

func TestNonCanonicalDeleteSetIsCaught(t *testing.T) {
	err := checkCanonicalDeleteSet(deleteSet{1: {{clock: 0, length: 2}, {clock: 2, length: 3}}})
	if err == nil || !strings.Contains(err.Error(), "touches") {
		t.Fatalf("two adjacent ranges gave %v", err)
	}
	if err := checkCanonicalDeleteSet(deleteSet{1: {{clock: 0, length: 2}, {clock: 3, length: 1}}}); err != nil {
		t.Fatalf("a canonical set was rejected: %v", err)
	}
}

func TestUnfoldedUpdateIsNotCanonical(t *testing.T) {
	whole := decoded{client: 42, parentName: "body", content: "hi", runeLen: 2, u16Len: 2}
	frags := map[ClientID][]decoded{42: splitRun(whole)}
	err := checkCanonicalUpdate(encodeStructs(frags, deleteSet{}))
	if err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("an unfolded update gave %v", err)
	}
	if err := checkCanonicalUpdate(encodeCanonical(frags, deleteSet{})); err != nil {
		t.Fatalf("a canonical update was rejected: %v", err)
	}
}
