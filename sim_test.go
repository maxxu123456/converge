package converge

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"testing"
)

var (
	simSeed  = flag.Int64("converge.seed", 1757974403, "seed for the randomized simulator")
	simSteps = flag.Int("converge.steps", 2000, "steps each simulated run takes")
)

// simAlphabet mixes ASCII, CJK, a combining mark and a non-BMP emoji, so the
// rune, byte and UTF-16 counts of a run are three different numbers.
var simAlphabet = []string{"a", "bc", "z ", "\u4e2d\u6587", "e\u0301", "\U0001F600", "\U0001F600\u4e2d"}

const (
	localOrigin = "local"
	peerOrigin  = "peer"
	seenKept    = 32 // recent deliveries kept around to deliver a second time
)

// source is where a run takes its choices: the rng under the simulator, the
// fuzzer's own bytes under FuzzConverge.
type source interface {
	intn(n int) int
	spent() bool
}

type randSource struct{ r *rand.Rand }

func (s randSource) intn(n int) int { return s.r.Intn(n) }
func (s randSource) spent() bool    { return false }

// scriptSource reads the choices out of a byte string, so a coverage-guided
// fuzzer can steer the run.
type scriptSource struct {
	b []byte
	i int
}

func (s *scriptSource) intn(n int) int {
	if n <= 1 || s.spent() {
		return 0
	}
	v := int(s.b[s.i]) % n
	s.i++
	return v
}

func (s *scriptSource) spent() bool { return s.i >= len(s.b) }

// replica is one simulated peer: its document, the updates in flight towards
// it, and the shadow texts its own deltas have to rebuild.
type replica struct {
	id      int
	doc     *Doc
	inbox   []Update // sent but not yet delivered
	seen    []Update // delivered, and available to deliver again
	shades  []*shadow
	offline int // steps left in a partition
}

// world drives a set of replicas that exchange only their own local updates,
// which is the mesh every peer-to-peer editor ends up with.
type world struct {
	t      *testing.T
	label  string
	src    source
	roots  []string
	reps   []*replica
	nextID ClientID
}

func newWorld(t *testing.T, label string, src source, n int, roots []string) *world {
	w := &world{t: t, label: label, src: src, roots: roots, nextID: 1}
	for i := 0; i < n; i++ {
		r := &replica{id: i}
		w.reload(r)
		w.reps = append(w.reps, r)
	}
	return w
}

func (w *world) fail(format string, args ...any) {
	w.t.Helper()
	w.t.Fatalf(w.label+": "+format, args...)
}

// reload gives r a fresh document with a fresh identity and loads whatever its
// old one held. A crash and a first start are the same thing here.
func (w *world) reload(r *replica) {
	var state Update
	if r.doc != nil {
		state = r.doc.EncodeStateAsUpdate(StateVector{})
	}
	r.doc = NewDocWith(Options{ClientID: w.nextID})
	w.nextID++
	r.shades = nil
	r.doc.OnUpdate(func(u Update, origin any) {
		// a peer forwards nothing: it sends its own work and nobody else's
		if origin == localOrigin {
			w.broadcast(r, u)
		}
	})
	for _, name := range w.roots {
		r.shades = append(r.shades, watch(w.t, r.doc.Text(name)))
	}
	if state != nil {
		w.apply(r, state)
	}
}

func (w *world) broadcast(from *replica, u Update) {
	if from.offline > 0 {
		return
	}
	for _, to := range w.reps {
		if to != from && to.offline == 0 {
			to.inbox = append(to.inbox, u)
		}
	}
}

func (w *world) apply(r *replica, u Update) {
	if err := r.doc.ApplyUpdate(u, peerOrigin); err != nil {
		w.fail("replica %d rejected an update: %v", r.id, err)
	}
	// a peer does not keep every message it ever received, and a run that did
	// would spend all its memory on old full-state updates
	if r.seen = append(r.seen, u); len(r.seen) > seenKept {
		r.seen = r.seen[len(r.seen)-seenKept:]
	}
}

// sync is the state vector handshake, which is how a peer catches up after a
// partition: an inbox it stopped receiving has nothing left to replay.
func (w *world) sync(a, b *replica) {
	toB := a.doc.EncodeStateAsUpdate(b.doc.StateVector())
	toA := b.doc.EncodeStateAsUpdate(a.doc.StateVector())
	w.apply(b, toB)
	w.apply(a, toA)
}

func (w *world) text(r *replica) *Text {
	// hoisted out of every Transact callback below: Doc.Text takes the lock the
	// transaction is already holding
	return r.doc.Text(w.roots[w.src.intn(len(w.roots))])
}

func (w *world) content() string { return simAlphabet[w.src.intn(len(simAlphabet))] }

func (w *world) insert(r *replica) {
	t := w.text(r)
	r.doc.Transact(localOrigin, func(tx *Tx) { tx.Insert(t, w.src.intn(tx.Len(t)+1), w.content()) })
}

func (w *world) remove(r *replica) {
	t := w.text(r)
	r.doc.Transact(localOrigin, func(tx *Tx) {
		n := tx.Len(t)
		if n == 0 {
			return
		}
		if w.src.intn(8) == 0 {
			tx.Delete(t, 0, n) // the whole document, tombstoning every run at once
			return
		}
		at := w.src.intn(n)
		tx.Delete(t, at, w.src.intn(n-at+1))
	})
}

// batch is several edits in one transaction, which must still produce one delta
// per Text and one update.
func (w *world) batch(r *replica) {
	t := w.text(r)
	ops := 2 + w.src.intn(4)
	r.doc.Transact(localOrigin, func(tx *Tx) {
		for i := 0; i < ops; i++ {
			n := tx.Len(t)
			if n > 0 && w.src.intn(3) == 0 {
				at := w.src.intn(n)
				tx.Delete(t, at, w.src.intn(n-at+1))
				continue
			}
			tx.Insert(t, w.src.intn(n+1), w.content())
		}
	})
}

// twoTexts touches both roots in one transaction: two deltas, one update.
func (w *world) twoTexts(r *replica) {
	if len(w.roots) < 2 {
		w.insert(r)
		return
	}
	first, second := r.doc.Text(w.roots[0]), r.doc.Text(w.roots[1])
	r.doc.Transact(localOrigin, func(tx *Tx) {
		tx.Insert(first, w.src.intn(tx.Len(first)+1), w.content())
		tx.Insert(second, w.src.intn(tx.Len(second)+1), w.content())
	})
}

func (w *world) deliver(r *replica) {
	if len(r.inbox) == 0 {
		return
	}
	u := r.inbox[0]
	r.inbox = r.inbox[1:]
	w.apply(r, u)
}

// deliverLate takes from the tail of the inbox, so a struct arrives before the
// one it was typed after.
func (w *world) deliverLate(r *replica) {
	if len(r.inbox) == 0 {
		return
	}
	i := len(r.inbox) - 1
	u := r.inbox[i]
	r.inbox = r.inbox[:i]
	w.apply(r, u)
}

func (w *world) deliverAgain(r *replica) {
	if len(r.seen) == 0 {
		return
	}
	w.apply(r, r.seen[w.src.intn(len(r.seen))])
}

func (w *world) disturb(r *replica) {
	switch w.src.intn(4) {
	case 0:
		r.offline = 2 + w.src.intn(15)
	case 1:
		w.reload(r)
	default:
		w.sync(r, w.reps[w.src.intn(len(w.reps))])
	}
}

func (w *world) step() {
	r := w.reps[w.src.intn(len(w.reps))]
	switch w.src.intn(12) {
	case 0, 1, 2:
		w.insert(r)
	case 3:
		w.remove(r)
	case 4:
		w.batch(r)
	case 5:
		w.twoTexts(r)
	case 6, 7, 8:
		w.deliver(r)
	case 9:
		w.deliverLate(r)
	case 10:
		w.deliverAgain(r)
	case 11:
		w.disturb(r)
	}
	w.check(r)
	for _, other := range w.reps {
		if other.offline > 0 {
			if other.offline--; other.offline == 0 {
				w.sync(other, w.reps[w.src.intn(len(w.reps))])
			}
		}
	}
}

// check runs after every step, not only at the end: a document that broke three
// hundred steps ago is unreadable by the time it diverges.
func (w *world) check(r *replica) {
	w.t.Helper()
	if err := r.doc.checkInvariants(); err != nil {
		w.fail("replica %d broke an invariant: %v", r.id, err)
	}
	for i, sh := range r.shades {
		sh.check(fmt.Sprintf("%s: replica %d root %q", w.label, r.id, w.roots[i]))
	}
}

// settle ends every partition, drains every inbox and then runs the handshake
// between every pair, which is what a real network does when the typing stops.
func (w *world) settle() {
	for _, r := range w.reps {
		r.offline = 0
	}
	for round := 0; round < 2; round++ {
		for _, r := range w.reps {
			for len(r.inbox) > 0 {
				w.deliver(r)
			}
		}
		for i, a := range w.reps {
			for _, b := range w.reps[i+1:] {
				w.sync(a, b)
			}
		}
	}
}

// converged is the whole claim under test: same text, same bytes, nothing left
// buffered anywhere.
func (w *world) converged() {
	w.t.Helper()
	base := w.reps[0]
	want := base.doc.EncodeStateAsUpdate(StateVector{})
	for _, r := range w.reps {
		w.check(r)
		if r.doc.pendingCount != 0 || !r.doc.pendingDS.empty() {
			w.fail("replica %d is stuck on %d structs and the deletes %v",
				r.id, r.doc.pendingCount, r.doc.pendingDS)
		}
		for _, name := range w.roots {
			got, exp := r.doc.Text(name), base.doc.Text(name)
			if got.String() != exp.String() {
				w.fail("replica %d reads %q in %q, replica 0 reads %q",
					r.id, got.String(), name, exp.String())
			}
			if got.Len() != exp.Len() || got.UTF16Len() != exp.UTF16Len() {
				w.fail("replica %d counts %d runes %d units in %q, replica 0 counts %d and %d",
					r.id, got.Len(), got.UTF16Len(), name, exp.Len(), exp.UTF16Len())
			}
		}
		// equal text and unequal bytes means the structure diverged underneath
		if got := r.doc.EncodeStateAsUpdate(StateVector{}); !bytes.Equal(got, want) {
			w.fail("replica %d encodes differently:\n%s\n%s", r.id, hexDump(got), hexDump(want))
		}
	}
	if len(w.roots) == 1 {
		// the naive one-item-per-rune oracle has to read the same document
		if got := modelText(w.t, want); got != base.doc.Text(w.roots[0]).String() {
			w.fail("the oracle reads %q, the replicas read %q", got, base.doc.Text(w.roots[0]).String())
		}
	}
}

func TestSimulatedReplicasConverge(t *testing.T) {
	cases := []struct {
		replicas int
		roots    []string
	}{
		{2, []string{"body"}},
		{3, []string{"body", "title"}},
		{5, []string{"body"}},
		{8, []string{"body", "title"}},
	}
	steps := *simSteps
	if testing.Short() {
		steps /= 10
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%dreplicas", c.replicas), func(t *testing.T) {
			label := fmt.Sprintf("seed %d, %d replicas", *simSeed, c.replicas)
			src := randSource{rand.New(rand.NewSource(*simSeed + int64(c.replicas)))}
			w := newWorld(t, label, src, c.replicas, c.roots)
			for i := 0; i < steps; i++ {
				w.step()
			}
			w.settle()
			w.converged()
		})
	}
}
