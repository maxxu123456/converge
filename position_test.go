package converge

import (
	"bytes"
	"errors"
	"testing"
)

// posPair returns two replicas sharing the text s in the root "body".
func posPair(t *testing.T, s string) (*Doc, *Text, *Doc, *Text) {
	t.Helper()
	a := NewDocWith(Options{ClientID: 1})
	b := NewDocWith(Options{ClientID: 2})
	ta, tb := a.Text("body"), b.Text("body")
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 0, s) })
	sendState(t, a, b)
	return a, ta, b, tb
}

// wantResolve fails unless p resolves in d to want, in the root it names.
func wantResolve(t *testing.T, d *Doc, p Position, want int) {
	t.Helper()
	text, index, ok := d.Resolve(p)
	if !ok {
		t.Fatalf("a position in %q did not resolve", p.TextName())
	}
	if text == nil || text.Name() != p.TextName() {
		t.Fatalf("a position in %q resolved into another root", p.TextName())
	}
	if index != want {
		t.Fatalf("a position in %q resolved to index %d, want %d", p.TextName(), index, want)
	}
}

func TestPositionRidesOutAConcurrentInsert(t *testing.T) {
	a, ta, b, tb := posPair(t, "hello world")
	p := ta.Position(6, AssocAfter) // the "w"
	b.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "XY") })
	sendState(t, b, a)
	validate(t, a)
	if got := ta.String(); got != "XYhello world" {
		t.Fatalf("the replica holds %q", got)
	}
	wantResolve(t, a, p, 8)
	wantResolve(t, b, p, 8)
}

func TestAssocBracketsAnInsertAtTheAnchor(t *testing.T) {
	a, ta, b, tb := posPair(t, "hello")
	before := ta.Position(2, AssocBefore)
	after := ta.Position(2, AssocAfter)
	b.Transact(nil, func(tx *Tx) { tx.Insert(tb, 2, "ZZ") })
	sendState(t, b, a)
	validate(t, a)
	if got := ta.String(); got != "heZZllo" {
		t.Fatalf("the replica holds %q", got)
	}
	// the two ends of a selection, with the inserted text between them
	wantResolve(t, a, before, 2)
	wantResolve(t, a, after, 4)
}

func TestSentinelsStayAtTheEnds(t *testing.T) {
	a, ta, b, tb := posPair(t, "hello")
	start := ta.Position(0, AssocBefore)
	end := ta.Position(ta.Len(), AssocAfter)
	if start.kind != posStart || end.kind != posEnd {
		t.Fatalf("the ends gave kinds %d and %d", start.kind, end.kind)
	}
	b.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "<") })
	b.Transact(nil, func(tx *Tx) { tx.Insert(tb, tx.Len(tb), ">") })
	sendState(t, b, a)
	validate(t, a)
	wantResolve(t, a, start, 0)
	wantResolve(t, a, end, ta.Len())
}

func TestPositionAtIndexZeroKeepsItsSide(t *testing.T) {
	a, ta, b, tb := posPair(t, "hello")
	// a left-associated cursor at 0 is pinned, a right-associated one rides
	// the first rune
	pinned := ta.Position(0, AssocBefore)
	riding := ta.Position(0, AssocAfter)
	b.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "AB") })
	sendState(t, b, a)
	wantResolve(t, a, pinned, 0)
	wantResolve(t, a, riding, 2)
}

func TestDeletedAnchorCollapsesToWhereTheTextWas(t *testing.T) {
	a, ta, b, tb := posPair(t, "hello world")
	p := ta.Position(6, AssocAfter) // the "w"
	b.Transact(nil, func(tx *Tx) { tx.Delete(tb, 4, 4) })
	sendState(t, b, a)
	validate(t, a)
	if got := ta.String(); got != "hellrld" {
		t.Fatalf("the replica holds %q", got)
	}
	// where the deleted runes were, with no +1: the cursor does not jump
	wantResolve(t, a, p, 4)
}

func TestAnchorThisReplicaLacksDoesNotResolve(t *testing.T) {
	a, _, b, tb := posPair(t, "hello")
	b.Transact(nil, func(tx *Tx) { tx.Insert(tb, 5, " there") })
	p := tb.Position(7, AssocAfter)
	text, index, ok := a.Resolve(p)
	if ok {
		t.Fatalf("a position on runes this replica lacks resolved to %d", index)
	}
	if text == nil {
		t.Fatal("the root is held here, so Resolve should still name it")
	}
	sendState(t, b, a)
	wantResolve(t, a, p, 7)
}

func TestPositionsThatNameNothingHere(t *testing.T) {
	a, ta, _, _ := posPair(t, "hello")
	anchored := ta.Position(1, AssocAfter)

	if _, _, ok := a.Resolve(Position{}); ok {
		t.Fatal("the zero Position resolved")
	}
	if text, _, ok := a.Resolve(Position{name: "absent", kind: posStart}); ok || text != nil {
		t.Fatal("a root this replica never saw resolved")
	}
	// the same anchor, claimed by a root it does not belong to
	a.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("title"), 0, "t") })
	wrong := anchored
	wrong.name = "title"
	if _, _, ok := a.Resolve(wrong); ok {
		t.Fatal("an anchor in another root resolved")
	}
}

func TestPositionClampsAndNormalizes(t *testing.T) {
	a, ta, _, _ := posPair(t, "hello")
	if got := ta.Position(-4, AssocBefore); got.kind != posStart {
		t.Fatalf("a negative index gave kind %d", got.kind)
	}
	if got := ta.Position(99, AssocAfter); got.kind != posEnd {
		t.Fatalf("an index past the end gave kind %d", got.kind)
	}
	if got := ta.Position(1, Assoc(7)); got.Assoc() != AssocAfter {
		t.Fatalf("an unknown assoc stayed %d", got.Assoc())
	}
	wantResolve(t, a, ta.Position(-4, AssocBefore), 0)
	wantResolve(t, a, ta.Position(99, AssocAfter), 5)

	empty := a.Text("empty")
	if got := empty.Position(0, AssocBefore); got.kind != posStart {
		t.Fatalf("index 0 of an empty text gave kind %d", got.kind)
	}
	if got := empty.Position(0, AssocAfter); got.kind != posEnd {
		t.Fatalf("index 0 of an empty text gave kind %d", got.kind)
	}
	wantResolve(t, a, empty.Position(0, AssocAfter), 0)
}

func TestPositionRoundTrip(t *testing.T) {
	_, ta, _, _ := posPair(t, "hello world")
	cases := []Position{
		ta.Position(0, AssocBefore),
		ta.Position(ta.Len(), AssocAfter),
		ta.Position(3, AssocAfter),
		ta.Position(3, AssocBefore),
		{name: "日本語", item: ID{Client: 1 << 40, Clock: 1 << 33}, kind: posAnchored, assoc: AssocBefore},
	}
	for _, p := range cases {
		raw, err := p.MarshalBinary()
		if err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
		var got Position
		if err := got.UnmarshalBinary(raw); err != nil {
			t.Fatalf("%+v encoded to %x, which does not decode: %v", p, raw, err)
		}
		if got != p {
			t.Fatalf("%+v round tripped to %+v", p, got)
		}
	}
}

func TestPositionBytes(t *testing.T) {
	p := Position{name: "body", item: ID{Client: 42, Clock: 1}, kind: posAnchored, assoc: AssocAfter}
	want := []byte{0xCF, 0x04, 0x01, 0x04, 'b', 'o', 'd', 'y', 0x02, 0x00, 0x2A, 0x01}
	got, _ := p.MarshalBinary()
	if !bytes.Equal(got, want) {
		t.Fatalf("encoded to %x, want %x", got, want)
	}
}

func TestMalformedPositionIsRejected(t *testing.T) {
	good, _ := Position{name: "body", item: ID{Client: 42, Clock: 1}, kind: posAnchored, assoc: AssocBefore}.MarshalBinary()
	bad := func(edit func(b []byte)) []byte {
		b := bytes.Clone(good)
		edit(b)
		return b
	}
	cases := []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"wrong magic", bad(func(b []byte) { b[0] = 0xCE })},
		{"another blob kind", bad(func(b []byte) { b[1] = kindUpdate })},
		{"empty name", []byte{0xCF, 0x04, 0x01, 0x00, 0x02, 0x00, 0x2A, 0x01}},
		{"unknown kind", bad(func(b []byte) { b[8] = 0x03 })},
		{"unknown assoc", bad(func(b []byte) { b[9] = 0x02 })},
		{"client zero", bad(func(b []byte) { b[10] = 0x00 })},
		{"trailing byte", append(bytes.Clone(good), 0x00)},
		{"sentinel with an id", []byte{0xCF, 0x04, 0x01, 0x01, 'b', 0x00, 0x00, 0x2A, 0x01}},
	}
	for _, c := range cases {
		var p Position
		err := p.UnmarshalBinary(c.in)
		if !errors.Is(err, ErrMalformedPosition) {
			t.Fatalf("%s: got %v, want ErrMalformedPosition", c.name, err)
		}
		if p.Valid() {
			t.Fatalf("%s: a rejected blob still filled the Position in", c.name)
		}
	}
	if err := (&Position{}).UnmarshalBinary(bad(func(b []byte) { b[2] = 0x02 })); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("a future version gave %v", err)
	}
	for i := 0; i < len(good); i++ {
		var p Position
		if err := p.UnmarshalBinary(good[:i]); err == nil {
			t.Fatalf("a %d byte prefix decoded", i)
		}
	}
}
