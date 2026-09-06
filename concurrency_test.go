package converge

import (
	"bytes"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mustFinish fails if f has not returned within a few seconds, which is what a
// document lock left held looks like from the outside.
func mustFinish(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never returned, the document lock is still held", what)
	}
}

// applyDelta splices ops into runes the way a binding would.
func applyDelta(runes []rune, ops []Delta) []rune {
	at := 0
	for _, op := range ops {
		switch {
		case op.Insert != "":
			r := []rune(op.Insert)
			runes = slices.Insert(runes, at, r...)
			at += len(r)
		case op.Delete > 0:
			runes = slices.Delete(runes, at, at+op.Delete)
		default:
			at += op.Retain
		}
	}
	return runes
}

func TestObserverCanCallBackIntoTheDoc(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	body := d.Text("body")
	var seen []string
	nested := false
	body.Observe(func(ev Event) {
		seen = append(seen, fmt.Sprint(ev.Delta))
		if body.Len() != len([]rune(body.String())) {
			t.Errorf("Len and String disagree inside an observer")
		}
		if nested {
			return
		}
		nested = true
		d.Transact("nested", func(tx *Tx) {
			b := tx.Text("body")
			tx.Insert(b, tx.Len(b), "N")
		})
		seen = append(seen, "nested returned")
	})
	mustFinish(t, "the reentrant transaction", func() {
		d.Transact(nil, func(tx *Tx) { tx.Insert(body, 0, "A") })
	})
	// the nested commit queues its event rather than delivering it inline,
	// so the marker lands between the two deltas
	want := []string{`[insert "A"]`, "nested returned", `[retain 1 insert "N"]`}
	if !slices.Equal(seen, want) {
		t.Fatalf("observer saw %q, want %q", seen, want)
	}
	if got := body.String(); got != "AN" {
		t.Fatalf("document holds %q, want %q", got, "AN")
	}
}

func TestObserverPanicLeavesTheDocUsable(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	body := d.Text("body")
	boom := body.Observe(func(Event) { panic("observer went bang") })
	survivor := 0
	body.Observe(func(Event) { survivor++ })
	mustFinish(t, "the panicking transaction", func() {
		defer func() {
			if recover() == nil {
				t.Errorf("the observer panic did not reach the writer")
			}
		}()
		d.Transact(nil, func(tx *Tx) { tx.Insert(body, 0, "a") })
	})
	boom()
	mustFinish(t, "the transaction after the panic", func() {
		d.Transact(nil, func(tx *Tx) { tx.Insert(body, 1, "b") })
	})
	if got := body.String(); got != "ab" {
		t.Fatalf("document holds %q, want %q", got, "ab")
	}
	if survivor != 1 {
		t.Fatalf("the surviving observer ran %d times, want 1", survivor)
	}
}

func TestCancelRacesDispatch(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	body := d.Text("body")
	const writes = 200
	done := make(chan struct{})
	var hits atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < writes; i++ {
			d.Transact(nil, func(tx *Tx) { tx.Insert(body, tx.Len(body), "x") })
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			cancel := body.Observe(func(Event) { hits.Add(1) })
			cancel()
			cancel()
		}
	}()
	wg.Wait()
	if got := body.Len(); got != writes {
		t.Fatalf("document holds %d runes, want %d", got, writes)
	}
	// one live observer at a time, so a cancelled one firing twice for a
	// notification would push this over
	if got := hits.Load(); got > writes {
		t.Fatalf("observers ran %d times over %d transactions", got, writes)
	}
	mustFinish(t, "a transaction after the churn", func() {
		d.Transact(nil, func(tx *Tx) { tx.Insert(body, 0, "y") })
	})
}

func TestEventsArriveInCommitOrder(t *testing.T) {
	d := NewDocWith(Options{ClientID: 1})
	body := d.Text("body")
	var seen []rune
	// the drain serializes the callbacks, so this needs no lock of its own
	body.Observe(func(ev Event) { seen = applyDelta(seen, ev.Delta) })
	const perWriter = 100
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(c rune) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				// insert in the middle, so a delta delivered out of order
				// lands its runes somewhere the document never put them
				d.Transact(nil, func(tx *Tx) { tx.Insert(body, tx.Len(body)/2, string(c)) })
			}
		}(rune('a' + w))
	}
	wg.Wait()
	if got, want := string(seen), body.String(); got != want {
		t.Fatalf("deltas in arrival order built %q, the document holds %q", got, want)
	}
	if body.Len() != 2*perWriter {
		t.Fatalf("document holds %d runes, want %d", body.Len(), 2*perWriter)
	}
}

// exerciseAPI runs one lap of everything a caller can reach: two replicas,
// local edits, a remote apply, observers, positions, encodings, read paths.
func exerciseAPI(t *testing.T) {
	t.Helper()
	a := NewDocWith(Options{ClientID: 1})
	b := NewDocWith(Options{ClientID: 2, MaxPendingStructs: 8})
	at, bt := a.Text("body"), b.Text("body")
	cancelUpd := a.OnUpdate(func(Update, any) {})
	cancelTxt := at.Observe(func(Event) {})
	a.Transact("typing", func(tx *Tx) {
		tx.Insert(at, 0, "hello world")
		tx.Insert(tx.Text("title"), 0, "\u4e2d\u6587")
		tx.Delete(at, 0, 1)
	})
	u := a.EncodeStateAsUpdate(b.StateVector())
	for i := 0; i < 2; i++ { // the second apply is the idempotence path
		if err := b.ApplyUpdate(u, "remote"); err != nil {
			t.Fatal(err)
		}
	}
	merged, err := MergeUpdates(u, b.EncodeStateAsUpdate(StateVector{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := merged.Diff(a.StateVector()); err != nil {
		t.Fatal(err)
	}
	if _, err := merged.StateVector(); err != nil {
		t.Fatal(err)
	}
	svBytes, err := a.StateVector().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseStateVector(svBytes); err != nil {
		t.Fatal(err)
	}
	posBytes, err := at.Position(2, AssocBefore).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var p Position
	if err := p.UnmarshalBinary(posBytes); err != nil {
		t.Fatal(err)
	}
	b.Resolve(p)
	var buf bytes.Buffer
	if _, err := bt.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	bt.Slice(0, bt.Len())
	bt.RuneIndex(bt.UTF16Index(1))
	bt.UTF16Len()
	b.Stats()
	b.Pending()
	a.ClientID()
	cancelTxt()
	cancelUpd()
}

func TestNoGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	exerciseAPI(t)
	// a goroutine an earlier test left unwinding can still be counted, so let
	// the scheduler drain before believing the number
	for i := 0; i < 1000 && runtime.NumGoroutine() > before; i++ {
		runtime.Gosched()
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("the goroutine count went from %d to %d", before, after)
	}
}
