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
