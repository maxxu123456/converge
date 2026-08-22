package converge

import (
	"fmt"
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
