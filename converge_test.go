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

// checkAgainstModel asserts every read accessor agrees with a plain []rune.
func checkAgainstModel(t *testing.T, tb *Text, model []rune) {
	t.Helper()
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
	insert(7, "世界")                   // CJK, three bytes per rune
	insert(0, "\U0001F600\U0001F389") // astral, two UTF-16 units per rune
	del(1, 1)                         // a whole astral rune, not a UTF-16 unit
	del(0, 1)
	del(3, 4)
	insert(len(model)/2, "́mid")
	del(len(model)-1, 1)
	del(0, len(model))
	insert(0, "again")
	del(0, 5)
}

func TestReadsIgnoreBlockBoundaries(t *testing.T) {
	d := NewDoc()
	tb := d.Text("body")
	d.Transact(nil, func(tx *Tx) {
		tx.Insert(tb, 0, "a\U0001F600b世c")
		tx.Delete(tb, 2, 1)
	})
	model := []rune("a\U0001F600世c")
	checkAgainstModel(t, tb, model)

	forceSplit(&d.store)
	checkAgainstModel(t, tb, model)

	// and an edit on the shattered list still lands where the model says
	d.Transact(nil, func(tx *Tx) { tx.Insert(tb, 2, "z") })
	model = []rune("a\U0001F600z世c")
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
		tx.Delete(tb, 0, 1)
	})
	var b strings.Builder
	n, err := tb.WriteTo(&b)
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if b.String() != "ello world" || n != int64(len("ello world")) {
		t.Fatalf("wrote %q (%d bytes), want %q", b.String(), n, "ello world")
	}

	w := &failingWriter{failOn: 2}
	n, err = tb.WriteTo(w)
	if !errors.Is(err, errWriterClosed) {
		t.Fatalf("error is %v, want %v", err, errWriterClosed)
	}
	if n != int64(len(w.wrote)) {
		t.Errorf("reported %d bytes after %d were written", n, len(w.wrote))
	}
	if len(w.wrote) == 0 || len(w.wrote) >= len("ello world") {
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
