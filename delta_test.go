package converge

import (
	"slices"
	"testing"
)

// shadow is a binding's view of a Text: a rune slice that only ever changes by
// applying the deltas the observer receives.
type shadow struct {
	t      *testing.T
	text   *Text
	runes  []rune
	events int
}

func watch(t *testing.T, text *Text) *shadow {
	s := &shadow{t: t, text: text}
	text.Observe(s.apply)
	return s
}

// apply is the naive delta interpreter, written without reference to anything
// the library knows about the list.
func (s *shadow) apply(ev Event) {
	s.t.Helper()
	if ev.Text != s.text {
		s.t.Fatalf("event for %q arrived at the observer of %q", ev.Text.Name(), s.text.Name())
	}
	if len(ev.Delta) == 0 {
		s.t.Fatalf("%q emitted an event with no ops", s.text.Name())
	}
	s.events++
	at := 0
	for _, op := range ev.Delta {
		switch {
		case op.Insert != "":
			r := []rune(op.Insert)
			s.runes = slices.Insert(s.runes, at, r...)
			at += len(r)
		case op.Delete > 0:
			if at+op.Delete > len(s.runes) {
				s.t.Fatalf("%v: %s runs past %q", ev.Delta, op, string(s.runes))
			}
			s.runes = slices.Delete(s.runes, at, at+op.Delete)
		case op.Retain > 0:
			at += op.Retain
			if at > len(s.runes) {
				s.t.Fatalf("%v: %s runs past %q", ev.Delta, op, string(s.runes))
			}
		default:
			s.t.Fatalf("%v: empty op", ev.Delta)
		}
	}
}

func (s *shadow) check(step string) {
	s.t.Helper()
	if got, want := string(s.runes), s.text.String(); got != want {
		s.t.Fatalf("%s: deltas built %q, the document holds %q", step, got, want)
	}
}

func TestDeltasRebuildTheDocument(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	body := d.Text("body")
	s := watch(t, body)
	steps := []struct {
		name string
		do   func(tx *Tx)
	}{
		{"first insert", func(tx *Tx) { tx.Insert(body, 0, "hello") }},
		{"append", func(tx *Tx) { tx.Insert(body, 5, " world") }},
		{"insert inside a run", func(tx *Tx) { tx.Insert(body, 5, ",") }},
		{"prepend", func(tx *Tx) { tx.Insert(body, 0, "> ") }},
		{"delete one rune", func(tx *Tx) { tx.Delete(body, 0, 1) }},
		{"delete across runs", func(tx *Tx) { tx.Delete(body, 4, 3) }},
		{"insert and delete together", func(tx *Tx) {
			tx.Delete(body, 0, 1)
			tx.Insert(body, 0, "H")
			tx.Insert(body, tx.Len(body), "!")
		}},
		{"delete either side of a tombstone", func(tx *Tx) {
			tx.Delete(body, 1, 2)
		}},
		{"astral plane", func(tx *Tx) { tx.Insert(body, 2, "\U0001F600\u00e9") }},
		{"delete everything", func(tx *Tx) { tx.Delete(body, 0, tx.Len(body)) }},
		{"write over the tombstones", func(tx *Tx) { tx.Insert(body, 0, "again") }},
	}
	for _, st := range steps {
		d.Transact(nil, st.do)
		s.check(st.name)
		validate(t, d)
	}
	if s.events != len(steps) {
		t.Fatalf("saw %d events for %d transactions", s.events, len(steps))
	}
}

func TestDeltasRebuildARemoteDocument(t *testing.T) {
	a := NewDocWith(Options{ClientID: 1})
	b := NewDocWith(Options{ClientID: 2})
	sa, sb := watch(t, a.Text("body")), watch(t, b.Text("body"))
	var wire []Update
	for _, d := range []*Doc{a, b} {
		d.OnUpdate(func(u Update, origin any) {
			if origin == nil { // only the local edits, never the replay
				wire = append(wire, slices.Clone(u))
			}
		})
	}
	edits := []struct {
		d *Doc
		f func(tx *Tx)
	}{
		{a, func(tx *Tx) { tx.Insert(tx.Text("body"), 0, "abc") }},
		{b, func(tx *Tx) { tx.Insert(tx.Text("body"), 0, "xyz") }},
		{a, func(tx *Tx) { tx.Insert(tx.Text("body"), 1, "-") }},
		{b, func(tx *Tx) { tx.Delete(tx.Text("body"), 0, 1) }},
	}
	for _, e := range edits {
		e.d.Transact(nil, e.f)
	}
	// each replica plays every update, its own included, so a duplicate has to
	// stay silent for the shadows to keep up
	for _, u := range wire {
		for _, d := range []*Doc{a, b} {
			if err := d.ApplyUpdate(u, "wire"); err != nil {
				t.Fatalf("apply: %v", err)
			}
		}
	}
	sa.check("replica a")
	sb.check("replica b")
	if a.Text("body").String() != b.Text("body").String() {
		t.Fatalf("replicas diverged: %q and %q", a.Text("body"), b.Text("body"))
	}
}

func TestDeltaCoversEveryChangedText(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	title, body := d.Text("title"), d.Text("body")
	st, sb := watch(t, title), watch(t, body)
	var order []string
	for _, x := range []*Text{title, body} {
		x.Observe(func(ev Event) { order = append(order, ev.Text.Name()) })
	}
	d.Transact(nil, func(tx *Tx) {
		tx.Insert(title, 0, "notes")
		tx.Insert(body, 0, "lorem")
		tx.Delete(title, 0, 1)
	})
	st.check("title")
	sb.check("body")
	if want := []string{"body", "title"}; !slices.Equal(order, want) {
		t.Fatalf("events arrived as %v, want %v", order, want)
	}
}

func TestDeltaSkipsTextNothingTouched(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	body := d.Text("body")
	s := watch(t, body)
	d.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("title"), 0, "notes") })
	if s.events != 0 {
		t.Fatalf("an untouched Text emitted %d events", s.events)
	}
	d.Transact(nil, func(tx *Tx) { tx.Insert(body, 0, "x") })
	if s.events != 1 {
		t.Fatalf("saw %d events, want 1", s.events)
	}
}

func TestDeltaHidesTextInsertedAndDeletedTogether(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	body := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(body, 0, "keep") })
	var deltas [][]Delta
	body.Observe(func(ev Event) { deltas = append(deltas, ev.Delta) })
	d.Transact(nil, func(tx *Tx) {
		tx.Insert(body, 4, "gone")
		tx.Delete(body, 4, 4)
	})
	if len(deltas) != 0 {
		t.Fatalf("text born and buried in one transaction emitted %v", deltas)
	}
	if body.String() != "keep" {
		t.Fatalf("document holds %q", body)
	}
}

func TestDeltaOpsAreCoalesced(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	body := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(body, 0, "abcdef") })
	var got []Delta
	body.Observe(func(ev Event) { got = ev.Delta })
	// two inserts at one caret fold into one op, while the retain between the
	// two deletes keeps those apart
	d.Transact(nil, func(tx *Tx) {
		tx.Delete(body, 1, 1)
		tx.Delete(body, 2, 1)
		tx.Insert(body, 0, "x")
		tx.Insert(body, 1, "y")
	})
	want := []Delta{{Insert: "xy"}, {Retain: 1}, {Delete: 1}, {Retain: 1}, {Delete: 1}}
	if !slices.Equal(got, want) {
		t.Fatalf("delta %v, want %v", got, want)
	}
	if body.String() != "xyacef" {
		t.Fatalf("document holds %q, want %q", body, "xyacef")
	}
}

func TestEventCarriesOriginAndLocality(t *testing.T) {
	a := NewDocWith(Options{ClientID: 1})
	b := NewDocWith(Options{ClientID: 2})
	var local, remote Event
	a.Text("body").Observe(func(ev Event) { local = ev })
	b.Text("body").Observe(func(ev Event) { remote = ev })
	var u Update
	a.OnUpdate(func(up Update, _ any) { u = slices.Clone(up) })
	a.Transact("editor", func(tx *Tx) { tx.Insert(tx.Text("body"), 0, "hi") })
	if !local.Local || local.Origin != "editor" {
		t.Fatalf("local event: Local=%v Origin=%v", local.Local, local.Origin)
	}
	if err := b.ApplyUpdate(u, "network"); err != nil {
		t.Fatal(err)
	}
	if remote.Local || remote.Origin != "network" {
		t.Fatalf("remote event: Local=%v Origin=%v", remote.Local, remote.Origin)
	}
	if !slices.Equal(remote.Delta, []Delta{{Insert: "hi"}}) {
		t.Fatalf("remote delta %v", remote.Delta)
	}
}

func TestCancelStopsAnObserver(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	body := d.Text("body")
	n := 0
	cancel := body.Observe(func(Event) { n++ })
	other := 0
	body.Observe(func(Event) { other++ })
	d.Transact(nil, func(tx *Tx) { tx.Insert(body, 0, "a") })
	cancel()
	cancel() // idempotent
	d.Transact(nil, func(tx *Tx) { tx.Insert(body, 1, "b") })
	if n != 1 {
		t.Fatalf("cancelled observer ran %d times", n)
	}
	if other != 2 {
		t.Fatalf("surviving observer ran %d times, want 2", other)
	}
}

func TestDeltaStringIsReadable(t *testing.T) {
	cases := []struct {
		op   Delta
		want string
	}{
		{Delta{Retain: 3}, "retain 3"},
		{Delta{Insert: "a\nb"}, `insert "a\nb"`},
		{Delta{Delete: 2}, "delete 2"},
		{Delta{}, "noop"},
	}
	for _, c := range cases {
		if got := c.op.String(); got != c.want {
			t.Errorf("%#v rendered %q, want %q", c.op, got, c.want)
		}
	}
}
