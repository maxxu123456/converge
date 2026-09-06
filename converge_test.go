package converge

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
)

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

// checkLinks asserts the list is doubly linked and agrees with the store. No
// read accessor walks left, so nothing else would notice a broken back link.
func checkLinks(t *testing.T, tb *Text) {
	t.Helper()
	var prev *item
	listed := 0
	for it := tb.start; it != nil; it = it.right {
		if it.left != prev {
			t.Fatalf("item %v does not point back at its predecessor", it.id)
		}
		if it.parent != tb {
			t.Fatalf("item %v hangs off another Text", it.id)
		}
		if got := tb.doc.store.get(it.id); got != it {
			t.Fatalf("the store answers %v with a different item", it.id)
		}
		prev = it
		listed++
	}
	stored := 0
	for _, cb := range tb.doc.store.clients {
		for _, it := range cb.blocks {
			if it.parent == tb {
				stored++
			}
		}
	}
	if listed != stored {
		t.Fatalf("the list holds %d items and the store %d", listed, stored)
	}
	checkContiguous(t, &tb.doc.store)
}

// checkAgainstModel asserts every read accessor agrees with a plain []rune.
func checkAgainstModel(t *testing.T, tb *Text, model []rune) {
	t.Helper()
	checkLinks(t, tb)
	want := string(model)
	if got := tb.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got := tb.Len(); got != len(model) {
		t.Fatalf("Len() = %d, want %d", got, len(model))
	}
	if tb.byteLen != len(want) {
		t.Fatalf("byteLen is %d, want %d", tb.byteLen, len(want))
	}
	units := utf16.Encode(model)
	if got := tb.UTF16Len(); got != len(units) {
		t.Fatalf("UTF16Len() = %d, want %d", got, len(units))
	}
	for a := 0; a <= len(model); a++ {
		for b := a; b <= len(model); b++ {
			if got, w := tb.Slice(a, b), string(model[a:b]); got != w {
				t.Fatalf("Slice(%d, %d) = %q, want %q", a, b, got, w)
			}
		}
		if got, w := tb.UTF16Index(a), len(utf16.Encode(model[:a])); got != w {
			t.Fatalf("UTF16Index(%d) = %d, want %d", a, got, w)
		}
	}
	for u := -1; u <= len(units)+2; u++ {
		w := 0
		for i := 0; i <= len(model); i++ {
			if len(utf16.Encode(model[:i])) <= u {
				w = i
			}
		}
		if got := tb.RuneIndex(u); got != w {
			t.Fatalf("RuneIndex(%d) = %d, want %d", u, got, w)
		}
	}
}

func TestInsertAndDeleteMatchARuneModel(t *testing.T) {
	d := NewDoc()
	tb := d.Text("body")
	var model []rune

	insert := func(index int, s string) {
		t.Helper()
		d.Transact(nil, func(tx *Tx) { tx.Insert(tb, index, s) })
		model = append(model[:index:index], append([]rune(s), model[index:]...)...)
		checkAgainstModel(t, tb, model)
	}
	del := func(index, length int) {
		t.Helper()
		d.Transact(nil, func(tx *Tx) { tx.Delete(tb, index, length) })
		model = append(model[:index:index], model[index+length:]...)
		checkAgainstModel(t, tb, model)
	}

	insert(0, "hello")
	insert(5, " world")
	insert(5, ",")
	insert(len(model), "!")
	insert(7, "\u4e16\u754c")         // CJK, three bytes per rune
	insert(0, "\U0001F600\U0001F389") // astral, two UTF-16 units per rune
	del(1, 1)                         // a whole astral rune, not a UTF-16 unit
	del(0, 1)
	del(3, 4)
	insert(len(model)/2, "\u0301mid")
	del(len(model)-1, 1)
	del(0, len(model))
	insert(0, "again")
	del(0, 5)
}

func TestReadsIgnoreBlockBoundaries(t *testing.T) {
	d := NewDoc()
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) {
		tx.Insert(tb, 0, "a\U0001F600b\u4e16c")
		tx.Delete(tb, 2, 1)
	})
	model := []rune("a\U0001F600\u4e16c")
	checkAgainstModel(t, tb, model)

	forceSplit(&d.store)
	checkAgainstModel(t, tb, model)

	// and an edit on the shattered list still lands where the model says
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 2, "z") })
	model = []rune("a\U0001F600z\u4e16c")
	checkAgainstModel(t, tb, model)
}

func TestEmptyInsertAndZeroLengthDeleteChangeNothing(t *testing.T) {
	d := NewDoc()
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "abc") })
	before := d.store.stateOf(d.clientID)
	items := itemCount(&d.store)

	d.Transact(nil, func(tx *Tx) {
		tx.Insert(tb, 0, "")
		tx.Insert(tb, 3, "")
		tx.Delete(tb, 1, 0)
		tx.Delete(tb, 1, -4)
	})
	if got := tb.String(); got != "abc" {
		t.Errorf("text is %q, want %q", got, "abc")
	}
	if got := d.store.stateOf(d.clientID); got != before {
		t.Errorf("the clock moved to %d, want %d", got, before)
	}
	if got := itemCount(&d.store); got != items {
		t.Errorf("the store holds %d items, want %d", got, items)
	}
}

func TestWriteTo(t *testing.T) {
	d := NewDoc()
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) {
		tx.Insert(tb, 0, "hello")
		tx.Insert(tb, 5, " world")
		tx.Delete(tb, 5, 1)
	})
	var b strings.Builder
	n, err := tb.WriteTo(&b)
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if b.String() != "helloworld" || n != int64(len("helloworld")) {
		t.Fatalf("wrote %q (%d bytes), want %q", b.String(), n, "helloworld")
	}

	w := &failingWriter{failOn: 2}
	n, err = tb.WriteTo(w)
	if !errors.Is(err, errWriterClosed) {
		t.Fatalf("error is %v, want %v", err, errWriterClosed)
	}
	if n != int64(len(w.wrote)) {
		t.Errorf("reported %d bytes after %d were written", n, len(w.wrote))
	}
	if len(w.wrote) == 0 || len(w.wrote) >= len("helloworld") {
		t.Errorf("wrote %q, want the part before the failure", w.wrote)
	}
}

var errWriterClosed = errors.New("test writer closed")

type failingWriter struct {
	failOn int
	calls  int
	wrote  []byte
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failOn {
		return 0, errWriterClosed
	}
	w.wrote = append(w.wrote, p...)
	return len(p), nil
}

func TestTextHandlesAreStable(t *testing.T) {
	d := NewDoc()
	tb := d.Text("body")
	if d.Text("body") != tb {
		t.Error("the second call returned a different handle")
	}
	if tb.Name() != "body" {
		t.Errorf("Name() = %q, want %q", tb.Name(), "body")
	}
	d.Transact(nil, func(tx *Tx) {
		if tx.Text("body") != tb {
			t.Error("Tx.Text returned a different handle")
		}
		tx.Insert(tx.Text("title"), 0, "notes")
		tx.Insert(tb, 0, "words")
	})
	if got := d.Text("title").String(); got != "notes" {
		t.Errorf("title is %q, want %q", got, "notes")
	}
	if got := tb.String(); got != "words" {
		t.Errorf("body is %q, want %q", got, "words")
	}
}

func TestTxReadsSeeTheTransactionSoFar(t *testing.T) {
	d := NewDoc()
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) {
		tx.Insert(tb, 0, "abcdef")
		if got := tx.Len(tb); got != 6 {
			t.Fatalf("Len = %d, want 6", got)
		}
		tx.Delete(tb, 1, 2)
		if got := tx.String(tb); got != "adef" {
			t.Fatalf("String = %q, want %q", got, "adef")
		}
		if got := tx.Slice(tb, 1, 3); got != "de" {
			t.Fatalf("Slice = %q, want %q", got, "de")
		}
		if tx.Doc() != d {
			t.Error("Tx.Doc returned another document")
		}
	})
}

func TestOriginReachesTheTransaction(t *testing.T) {
	d := NewDoc()
	want := "editor"
	d.Transact(want, func(tx *Tx) {
		if got := tx.Origin(); got != want {
			t.Errorf("Origin() = %v, want %v", got, want)
		}
	})
}

// recovered runs fn and returns what it panicked with, or nil.
func recovered(fn func()) (v any) {
	defer func() { v = recover() }()
	fn()
	return nil
}

func wantRangeError(t *testing.T, v any, index, length, textLen int) {
	t.Helper()
	re, ok := v.(*RangeError)
	if !ok {
		t.Fatalf("panicked with %v (%T), want *RangeError", v, v)
	}
	if re.Index != index || re.Length != length || re.Len != textLen {
		t.Errorf("RangeError{index %d, length %d, len %d}, want {%d, %d, %d}",
			re.Index, re.Length, re.Len, index, length, textLen)
	}
	if re.Text != "body" {
		t.Errorf("RangeError names %q, want %q", re.Text, "body")
	}
	if _, isErr := v.(error); !isErr {
		t.Error("the panic value does not implement error")
	}
}

func wantUsageError(t *testing.T, v any, msg string) {
	t.Helper()
	ue, ok := v.(*UsageError)
	if !ok {
		t.Fatalf("panicked with %v (%T), want *UsageError", v, v)
	}
	if !strings.Contains(ue.Error(), msg) {
		t.Errorf("message %q does not mention %q", ue.Error(), msg)
	}
}

func TestInsertAndDeleteRangePanics(t *testing.T) {
	d := NewDoc()
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "abc") })

	wantRangeError(t, recovered(func() {
		d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 4, "x") })
	}), 4, 0, 3)
	wantRangeError(t, recovered(func() {
		d.Transact(nil, func(tx *Tx) { tx.Insert(tb, -1, "x") })
	}), -1, 0, 3)
	wantRangeError(t, recovered(func() {
		d.Transact(nil, func(tx *Tx) { tx.Delete(tb, 2, 2) })
	}), 2, 2, 3)
	wantRangeError(t, recovered(func() {
		d.Transact(nil, func(tx *Tx) { tx.Delete(tb, -1, 1) })
	}), -1, 1, 3)

	// the index is checked before anything is mutated, and the Doc still works
	if got := tb.String(); got != "abc" {
		t.Fatalf("text is %q, want %q", got, "abc")
	}
	if itemCount(&d.store) != 1 {
		t.Fatalf("a rejected op left %d items behind", itemCount(&d.store))
	}
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 3, "d") })
	if got := tb.String(); got != "abcd" {
		t.Fatalf("text is %q, want %q", got, "abcd")
	}
}

func TestSliceAndUTF16IndexPanics(t *testing.T) {
	d := NewDoc()
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "abc") })

	wantRangeError(t, recovered(func() { tb.Slice(-1, 2) }), -1, 3, 3)
	wantRangeError(t, recovered(func() { tb.Slice(1, 4) }), 1, 3, 3)
	wantRangeError(t, recovered(func() { tb.Slice(2, 1) }), 2, -1, 3)
	wantRangeError(t, recovered(func() { tb.UTF16Index(4) }), 4, 0, 3)
	wantRangeError(t, recovered(func() { tb.UTF16Index(-1) }), -1, 0, 3)
	wantRangeError(t, recovered(func() {
		d.Transact(nil, func(tx *Tx) { tx.Slice(tb, 0, 9) })
	}), 0, 9, 3)

	// a failed read must not have wedged the lock
	if got := tb.Slice(0, 3); got != "abc" {
		t.Fatalf("Slice = %q, want %q", got, "abc")
	}
}

func TestInsertRejectsInvalidUTF8(t *testing.T) {
	d := NewDoc()
	tb := d.Text("body")
	wantUsageError(t, recovered(func() {
		d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "ok\xffbad") })
	}), "valid UTF-8")
	if tb.Len() != 0 {
		t.Errorf("the document kept %d runes", tb.Len())
	}
}

func TestTextFromAnotherDocPanics(t *testing.T) {
	a, b := NewDoc(), NewDoc()
	other := b.Text("body")
	wantUsageError(t, recovered(func() {
		a.Transact(nil, func(tx *Tx) { tx.Insert(other, 0, "x") })
	}), "different Doc")
	wantUsageError(t, recovered(func() {
		a.Transact(nil, func(tx *Tx) { tx.Delete(other, 0, 1) })
	}), "different Doc")
	wantUsageError(t, recovered(func() {
		a.Transact(nil, func(tx *Tx) { tx.String(other) })
	}), "different Doc")
	if other.Len() != 0 {
		t.Errorf("the other document changed to %q", other.String())
	}
	// both documents are still usable
	a.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("body"), 0, "a") })
	b.Transact(nil, func(tx *Tx) { tx.Insert(other, 0, "b") })
	if a.Text("body").String() != "a" || other.String() != "b" {
		t.Errorf("documents read %q and %q", a.Text("body").String(), other.String())
	}
}

func TestTxUsedAfterItsCallbackPanics(t *testing.T) {
	d := NewDoc()
	tb := d.Text("body")
	var escaped *Tx
	d.Transact(nil, func(tx *Tx) {
		escaped = tx
		tx.Insert(tb, 0, "abc")
	})
	for _, c := range []struct {
		name string
		fn   func()
	}{
		{"Insert", func() { escaped.Insert(tb, 0, "x") }},
		{"Delete", func() { escaped.Delete(tb, 0, 1) }},
		{"Len", func() { escaped.Len(tb) }},
		{"String", func() { escaped.String(tb) }},
		{"Slice", func() { escaped.Slice(tb, 0, 1) }},
		{"Text", func() { escaped.Text("body") }},
		{"Origin", func() { escaped.Origin() }},
		{"Doc", func() { escaped.Doc() }},
	} {
		wantUsageError(t, recovered(c.fn), "after its callback")
		if got := tb.String(); got != "abc" {
			t.Fatalf("%s changed the text to %q", c.name, got)
		}
	}
}

func TestRootNameMustBeShortValidUTF8(t *testing.T) {
	d := NewDoc()
	for _, name := range []string{"", strings.Repeat("n", 256), "bad\xffname"} {
		wantUsageError(t, recovered(func() { d.Text(name) }), "root name")
		wantUsageError(t, recovered(func() {
			d.Transact(nil, func(tx *Tx) { tx.Text(name) })
		}), "root name")
	}
	if got := d.Text(strings.Repeat("n", 255)); got.Name() != strings.Repeat("n", 255) {
		t.Error("a 255 byte name was rejected")
	}
}

func TestPanicInCallbackKeepsWhatItApplied(t *testing.T) {
	d := NewDoc()
	tb := d.Text("body")
	boom := errors.New("editor blew up")
	if got := recovered(func() {
		d.Transact(nil, func(tx *Tx) {
			tx.Insert(tb, 0, "kept")
			panic(boom)
		})
	}); got != boom {
		t.Fatalf("panicked with %v, want %v", got, boom)
	}
	if d.txOpen {
		t.Error("the transaction is still open")
	}
	// the lock was released and the edit before the panic survived
	if got := tb.String(); got != "kept" {
		t.Fatalf("text is %q, want %q", got, "kept")
	}
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 4, "more") })
	if got := tb.String(); got != "keptmore" {
		t.Fatalf("text is %q, want %q", got, "keptmore")
	}
}

func TestInsertRecordsTheNeighboursItWasBornWith(t *testing.T) {
	d := NewDocWith(Options{ClientID: 9})
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "abc") })
	if first := tb.start; !first.origin.IsZero() || !first.rightOrigin.IsZero() {
		t.Fatalf("the first item was born with origins %v and %v", first.origin, first.rightOrigin)
	}

	// appending after a run anchors on the run's LAST rune, not on its first
	var z *item
	d.Transact(nil, func(tx *Tx) {
		tx.Insert(tb, 3, "Z")
		z = d.store.get(ID{9, 3}) // read here: the merge pass folds Z back into abc
	})
	if want := (ID{9, 2}); z.origin != want {
		t.Errorf("origin %v, want %v", z.origin, want)
	}
	if !z.rightOrigin.IsZero() {
		t.Errorf("rightOrigin %v, want none", z.rightOrigin)
	}
	checkAgainstModel(t, tb, []rune("abcZ"))

	var x *item
	d.Transact(nil, func(tx *Tx) {
		tx.Insert(tb, 1, "X")
		x = d.store.get(ID{9, 4})
	})
	if want := (ID{9, 0}); x.origin != want {
		t.Errorf("origin %v, want %v", x.origin, want)
	}
	if want := (ID{9, 1}); x.rightOrigin != want {
		t.Errorf("rightOrigin %v, want %v", x.rightOrigin, want)
	}
	checkAgainstModel(t, tb, []rune("aXbcZ"))

	// deleting the right neighbour must not change what the next insert anchors to
	d.Transact(nil, func(tx *Tx) { tx.Delete(tb, 2, 1) })
	var y *item
	d.Transact(nil, func(tx *Tx) {
		tx.Insert(tb, 2, "Y")
		y = d.store.get(ID{9, 5})
	})
	if want := (ID{9, 4}); y.origin != want {
		t.Errorf("origin %v, want %v", y.origin, want)
	}
	if want := (ID{9, 1}); y.rightOrigin != want {
		t.Errorf("rightOrigin %v, want the tombstone %v", y.rightOrigin, want)
	}
	checkAgainstModel(t, tb, []rune("aXYcZ"))
}

func TestRepeatedTombstoneChangesNothing(t *testing.T) {
	d := NewDocWith(Options{ClientID: 5})
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "abc") })
	d.Transact(nil, func(tx *Tx) {
		it := tb.start
		deleteItem(tx, it)
		deleteItem(tx, it)
	})
	if tb.Len() != 0 || tb.byteLen != 0 || tb.UTF16Len() != 0 {
		t.Fatalf("counters read %d runes, %d bytes, %d units",
			tb.Len(), tb.byteLen, tb.UTF16Len())
	}
	if got := d.tx.deleted[5]; len(got) != 1 || got[0] != (idRange{0, 3}) {
		t.Errorf("transitions %v, want one range covering three clocks", got)
	}
}

func TestOnUpdateFiresOncePerChangedTransaction(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	tb := d.Text("body")
	var origins []any
	cancel := d.OnUpdate(func(u Update, origin any) {
		if len(u) == 0 {
			t.Error("an observer was handed an empty update")
		}
		origins = append(origins, origin)
	})

	d.Transact("typing", func(tx *Tx) {
		tx.Insert(tb, 0, "ab")
		tx.Insert(tb, 2, "cd")
		tx.Delete(tb, 0, 1)
	})
	if len(origins) != 1 || origins[0] != "typing" {
		t.Fatalf("one transaction produced %v", origins)
	}

	// no-ops change no clock and no tombstone, so they emit nothing
	d.Transact("idle", func(tx *Tx) {
		tx.Insert(tb, 0, "")
		tx.Delete(tb, 0, 0)
	})
	if len(origins) != 1 {
		t.Fatalf("a transaction that changed nothing emitted %v", origins[1:])
	}

	cancel()
	cancel()
	d.Transact("typing", func(tx *Tx) { tx.Insert(tb, 0, "z") })
	if len(origins) != 1 {
		t.Fatalf("a cancelled observer fired again: %v", origins[1:])
	}
}

func TestStatsCountsTombstonesAndBytes(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 0, "h\u00e9llo") })
	d.Transact(nil, func(tx *Tx) { tx.Delete(tb, 0, 2) })

	want := Stats{Clients: 1, Items: 2, VisibleRunes: 3, TombstoneRunes: 2, ContentBytes: 6}
	if got := d.Stats(); got != want {
		t.Fatalf("stats %+v, want %+v", got, want)
	}

	e := NewDocWith(Options{ClientID: 2})
	eb := e.Text("body")
	e.Transact(nil, func(tx *Tx) { tx.Insert(eb, 0, "ab") })
	if err := d.ApplyUpdate(e.EncodeStateAsUpdate(StateVector{}), nil); err != nil {
		t.Fatal(err)
	}
	// a second replica's runs are held in the same store and counted the same way
	want = Stats{Clients: 2, Items: 3, VisibleRunes: 5, TombstoneRunes: 2, ContentBytes: 8}
	if got := d.Stats(); got != want {
		t.Fatalf("stats %+v, want %+v", got, want)
	}
	Validate(t, d)
}

// chainOfThree returns three replicas that each typed one rune after the one
// before it, so an update from the last two is blocked on the first.
func chainOfThree(t *testing.T) (*Doc, *Doc, *Doc) {
	t.Helper()
	a := NewDocWith(Options{ClientID: 1})
	b := NewDocWith(Options{ClientID: 2})
	c := NewDocWith(Options{ClientID: 3})
	a.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("body"), 0, "a") })
	sendState(t, a, b)
	b.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("body"), 1, "b") })
	sendState(t, b, c)
	c.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("body"), 2, "c") })
	return a, b, c
}

func TestPendingOverflowDoesNotEvict(t *testing.T) {
	a, b, c := chainOfThree(t)
	tail, err := MergeUpdates(
		b.EncodeStateAsUpdate(a.StateVector()),
		c.EncodeStateAsUpdate(b.StateVector()),
	)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	d := NewDocWith(Options{ClientID: 4, MaxPendingStructs: 1})
	if err := d.ApplyUpdate(tail, "peer"); !errors.Is(err, ErrPendingOverflow) {
		t.Fatalf("applying two blocked structs under a cap of one gave %v", err)
	}
	Validate(t, d)
	if d.pendingCount != 1 {
		t.Fatalf("the buffer holds %d structs, want the one that fit", d.pendingCount)
	}
	if got := d.Text("body").String(); got != "" {
		t.Fatalf("a blocked update left %q behind", got)
	}

	// nothing was evicted, so a fresh handshake is all the recovery there is
	sendState(t, c, d)
	Validate(t, d)
	if got := d.Text("body").String(); got != "abc" {
		t.Fatalf("after the resync %q, want %q", got, "abc")
	}
	if d.pendingCount != 0 {
		t.Fatalf("%d structs are still buffered after the resync", d.pendingCount)
	}
}

func TestPendingNamesTheClockItWaitsFor(t *testing.T) {
	_, b, c := chainOfThree(t)
	// only the last rune, which hangs off a rune of b this replica lacks
	d := NewDocWith(Options{ClientID: 4})
	if err := d.ApplyUpdate(c.EncodeStateAsUpdate(b.StateVector()), "peer"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	Validate(t, d)
	n, missing := d.Pending()
	if n != 1 {
		t.Fatalf("%d structs buffered, want 1", n)
	}
	if got := missing.Get(2); got != 1 {
		t.Fatalf("waiting for client 2 from clock %d, want 1", got)
	}
	if got := missing.Get(1); got != 0 {
		t.Fatalf("client 1 is named at clock %d, but nothing buffered asks for it", got)
	}
	if got := d.Stats().PendingStructs; got != n {
		t.Fatalf("stats say %d pending, Pending says %d", got, n)
	}

	// the backlog clears itself once what it named arrives
	sendState(t, b, d)
	Validate(t, d)
	if n, missing := d.Pending(); n != 0 || len(missing.Clients()) != 0 {
		t.Fatalf("still waiting on %d structs %v", n, missing)
	}
	if got := d.Text("body").String(); got != "abc" {
		t.Fatalf("text %q, want %q", got, "abc")
	}
}

func TestPendingNamesTheLowestUnblockingClock(t *testing.T) {
	x := NewDocWith(Options{ClientID: 1})
	tx0 := x.Text("body")
	x.Transact(nil, func(tx *Tx) { tx.Insert(tx0, 0, "0123456789") })

	// two replicas type into x's text at different points, so their structs
	// name two different clocks of the same client
	early := NewDocWith(Options{ClientID: 2})
	late := NewDocWith(Options{ClientID: 3})
	sendState(t, x, early)
	sendState(t, x, late)
	early.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("body"), 2, "E") })
	late.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("body"), 9, "L") })

	d := NewDocWith(Options{ClientID: 4})
	for _, u := range []Update{
		late.EncodeStateAsUpdate(x.StateVector()),
		early.EncodeStateAsUpdate(x.StateVector()),
	} {
		if err := d.ApplyUpdate(u, "peer"); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	Validate(t, d)
	n, missing := d.Pending()
	if n != 2 {
		t.Fatalf("%d structs buffered, want 2", n)
	}
	// the earlier struct needs clocks 1 and 2 of client 1, the later one 8 and
	// 9, and the lowest is the one a peer is most likely to still hold
	if got := missing.Get(1); got != 2 {
		t.Fatalf("waiting for client 1 from clock %d, want 2", got)
	}
}

// TestReturnedSlicesAreOwned scribbles over every slice converge hands back,
// then asks the document and its encodings whether they noticed.
func TestReturnedSlicesAreOwned(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	body := d.Text("body")
	var delivered []Delta
	var relayed Update
	d.OnUpdate(func(u Update, _ any) { relayed = u })
	body.Observe(func(ev Event) { delivered = ev.Delta })
	d.Transact(nil, func(tx *Tx) { tx.Insert(body, 0, "hello world") })
	d.Transact(nil, func(tx *Tx) { tx.Delete(body, 0, 6) })

	text := body.String()
	update := d.EncodeStateAsUpdate(StateVector{})
	wantUpdate := string(update)
	svBytes, err := d.StateVector().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	wantSV := string(svBytes)
	posBytes, err := body.Position(2, AssocAfter).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	wantPos := string(posBytes)
	clients := d.StateVector().Clients()
	if len(delivered) == 0 || len(relayed) == 0 || len(clients) == 0 {
		t.Fatal("the observers came back empty, so this test proves nothing")
	}

	for _, b := range [][]byte{update, svBytes, posBytes, relayed} {
		for i := range b {
			b[i] = 0xff
		}
	}
	for i := range delivered {
		delivered[i] = Delta{Retain: 1 << 30}
	}
	for i := range clients {
		clients[i] = 0
	}

	if got := body.String(); got != text {
		t.Errorf("the document reads %q, want %q", got, text)
	}
	if got := string(d.EncodeStateAsUpdate(StateVector{})); got != wantUpdate {
		t.Error("the update encoding changed")
	}
	if got, _ := d.StateVector().MarshalBinary(); string(got) != wantSV {
		t.Error("the state vector encoding changed")
	}
	if got, _ := body.Position(2, AssocAfter).MarshalBinary(); string(got) != wantPos {
		t.Error("the position encoding changed")
	}
	validate(t, d)
}
