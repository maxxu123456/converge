package converge

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// exAction is one step of a trace: an edit by one client, or the delivery of
// another client's state to it.
type exAction struct {
	kind  byte // 'i' insert, 'd' delete, 'r' receive
	n     int  // runes to insert or delete, or the peer offset to receive from
	where int  // rune index, negative for the end
}

const exClients = 3

// exWide covers the edit shapes. The deep alphabets drop most of them to buy
// one more action: one stacks runes at a caret, one anchors inside a run.
var (
	exWide = []exAction{
		{'i', 1, 0}, {'i', 1, 1}, {'i', 1, -2}, {'i', 1, -1},
		{'i', 2, 1}, {'i', 2, -1},
		{'d', 1, 0}, {'d', 1, 1},
		{'r', 1, 0}, {'r', 2, 0},
	}
	exCarets = []exAction{{'i', 1, 1}, {'i', 1, -1}, {'r', 1, 0}, {'r', 2, 0}}
	exRuns   = []exAction{{'i', 2, 1}, {'i', 1, -2}, {'r', 1, 0}, {'r', 2, 0}}
	exBases  = []string{"", "AB"}
	exOrders = [6][exClients]int{
		{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0},
	}
)

// TestExhaustiveDeliveryInterleavings generates every trace over a bounded
// alphabet: each delivery order of the three states, one of them delivered
// twice, must read what the oracle reads. Random traces hit these shapes rarely.
func TestExhaustiveDeliveryInterleavings(t *testing.T) {
	wide, deep := 6000, 7000
	if testing.Short() {
		wide, deep = 600, 700
	}
	n := exSearch(t, exWide, 3, wide)
	n += exSearch(t, exCarets, 4, deep)
	n += exSearch(t, exRuns, 4, deep)
	t.Logf("checked %d traces", n)
}

// exSearch walks every trace of up to maxOps actions, shortest first, and
// stops once budget traces have run.
func exSearch(t *testing.T, alphabet []exAction, maxOps, budget int) int {
	t.Helper()
	ran := 0
	choices := exClients * len(alphabet)
	for ops := 1; ops <= maxOps && ran < budget; ops++ {
		seq := make([]int, ops)
		for {
			for _, base := range exBases {
				exTrace(t, base, alphabet, seq)
				ran++
			}
			if ran >= budget || !exNext(seq, choices) {
				break
			}
		}
	}
	return ran
}

// exNext counts seq up by one in base radix, most significant digit first, and
// reports whether it wrapped.
func exNext(seq []int, radix int) bool {
	for i := len(seq) - 1; i >= 0; i-- {
		if seq[i]++; seq[i] < radix {
			return true
		}
		seq[i] = 0
	}
	return false
}

// exTrace plays one trace and checks every delivery order of the three
// resulting states.
func exTrace(t *testing.T, base string, alphabet []exAction, seq []int) {
	t.Helper()
	docs := make([]*Doc, exClients)
	for i := range docs {
		docs[i] = NewDocWith(Options{ClientID: ClientID(2 + i)})
	}
	if base != "" {
		b := NewDocWith(Options{ClientID: 1})
		b.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("body"), 0, base) })
		u := b.EncodeStateAsUpdate(StateVector{})
		for _, d := range docs {
			if err := d.ApplyUpdate(u, "base"); err != nil {
				t.Fatalf("%s: the base was rejected: %v", exName(base, alphabet, seq), err)
			}
		}
	}
	for _, step := range seq {
		if err := exStep(docs, step/len(alphabet), alphabet[step%len(alphabet)]); err != nil {
			t.Fatalf("%s: %v", exName(base, alphabet, seq), err)
		}
	}
	updates := make([]Update, exClients)
	for i, d := range docs {
		updates[i] = d.EncodeStateAsUpdate(StateVector{})
	}
	exConverge(t, exName(base, alphabet, seq), updates)
}

func exStep(docs []*Doc, actor int, a exAction) error {
	d := docs[actor]
	tb := d.Text("body")
	switch a.kind {
	case 'r':
		peer := docs[(actor+a.n)%exClients]
		if err := d.ApplyUpdate(peer.EncodeStateAsUpdate(d.StateVector()), "peer"); err != nil {
			return fmt.Errorf("client %d rejected a peer's state: %w", actor, err)
		}
	case 'i':
		d.Transact(nil, func(tx *Tx) {
			tx.Insert(tb, exIndex(tx.Len(tb), a.where), strings.Repeat(string(rune('x'+actor)), a.n))
		})
	case 'd':
		d.Transact(nil, func(tx *Tx) {
			at := exIndex(tx.Len(tb), a.where)
			if at+a.n <= tx.Len(tb) {
				tx.Delete(tb, at, a.n)
			}
		})
	}
	return nil
}

// exIndex resolves a trace index: negative counts back from the end, so -1
// is the end of the text and -2 the caret one rune before it.
func exIndex(length, where int) int {
	if where < 0 {
		where += length + 1
	}
	return min(max(where, 0), length)
}

// exConverge delivers updates to one replica per order, validating after every
// delivery, and requires identical bytes and the oracle's text everywhere.
func exConverge(t *testing.T, name string, updates []Update) {
	t.Helper()
	var want string
	var wantBytes []byte
	var wantOrder [exClients]int
	for k, order := range exOrders {
		r := NewDocWith(Options{ClientID: ClientID(10 + k)})
		for i, u := range exDeliveries(order, k%exClients, updates) {
			if err := r.ApplyUpdate(u, "peer"); err != nil {
				t.Fatalf("%s: order %v delivery %d: %v", name, order, i, err)
			}
			if err := r.checkInvariants(); err != nil {
				t.Fatalf("%s: order %v after delivery %d: %v", name, order, i, err)
			}
		}
		got := r.Text("body").String()
		gotBytes := r.EncodeStateAsUpdate(StateVector{})
		if k == 0 {
			want, wantBytes, wantOrder = got, gotBytes, order
		} else if got != want || !bytes.Equal(gotBytes, wantBytes) {
			t.Fatalf("%s: order %v reads %q, order %v reads %q\n%s\n%s",
				name, order, got, wantOrder, want, hexDump(gotBytes), hexDump(wantBytes))
		}
		m := newModel()
		for i := range updates {
			if err := m.apply(updates[order[i]]); err != nil {
				t.Fatalf("%s: the oracle rejected update %d: %v", name, i, err)
			}
		}
		if oracle := m.text(); oracle != got {
			t.Fatalf("%s: order %v reads %q, the oracle reads %q", name, order, got, oracle)
		}
	}
}

// exDeliveries lists the updates in order, with one of them delivered twice.
func exDeliveries(order [exClients]int, dup int, updates []Update) []Update {
	out := make([]Update, 0, exClients+1)
	for i, idx := range order {
		out = append(out, updates[idx])
		if i == dup {
			out = append(out, updates[idx])
		}
	}
	return out
}

// exName renders a trace as the base plus one client and action per step, so a
// failure names something a person can replay.
func exName(base string, alphabet []exAction, seq []int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "base %q:", base)
	for _, step := range seq {
		a := alphabet[step%len(alphabet)]
		fmt.Fprintf(&b, " c%d %c%d@%d", step/len(alphabet), a.kind, a.n, a.where)
	}
	return b.String()
}
