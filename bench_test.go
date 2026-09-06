package converge

import (
	"math/rand"
	"strings"
	"testing"
)

// benchRunes is the crdt-benchmarks document size. Big enough that an
// accidentally quadratic path reads as a wall rather than as noise.
const benchRunes = 100000

var (
	benchString string
	benchUpdate Update
)

// benchDoc builds an n-rune document in 200-rune bursts at random positions,
// which leaves roughly the run count a real session does.
func benchDoc(n int) (*Doc, *Text) {
	d := NewDocWith(Options{ClientID: 1})
	t := d.Text("body")
	rng := rand.New(rand.NewSource(7))
	chunk := strings.Repeat("x", 200)
	for t.Len() < n {
		d.Transact(nil, func(tx *Tx) { tx.Insert(t, rng.Intn(tx.Len(t)+1), chunk) })
	}
	return d, t
}

func BenchmarkSequentialTyping(b *testing.B) {
	for i := 0; i < b.N; i++ {
		d := NewDocWith(Options{ClientID: 1})
		t := d.Text("body")
		for j := 0; j < benchRunes; j++ {
			d.Transact(nil, func(tx *Tx) { tx.Insert(t, tx.Len(t), "a") })
		}
	}
}

// BenchmarkRemoteWhileTyping types locally between remote inserts at the head,
// so every keystroke lands after the search markers have had to move. A marker
// cache that invalidated instead of adjusting still passes every correctness
// test and turns only this one quadratic.
func BenchmarkRemoteWhileTyping(b *testing.B) {
	const n = benchRunes / 10
	for i := 0; i < b.N; i++ {
		local := NewDocWith(Options{ClientID: 1})
		remote := NewDocWith(Options{ClientID: 2})
		lt, rt := local.Text("body"), remote.Text("body")
		var last Update
		cancel := remote.OnUpdate(func(u Update, _ any) { last = u })
		for j := 0; j < n; j++ {
			local.Transact(nil, func(tx *Tx) { tx.Insert(lt, tx.Len(lt), "a") })
			remote.Transact(nil, func(tx *Tx) { tx.Insert(rt, 0, "b") })
			if err := local.ApplyUpdate(last, "remote"); err != nil {
				b.Fatal(err)
			}
		}
		cancel()
	}
}

func BenchmarkRandomInsert(b *testing.B) {
	const n = benchRunes / 10
	for i := 0; i < b.N; i++ {
		d := NewDocWith(Options{ClientID: 1})
		t := d.Text("body")
		rng := rand.New(rand.NewSource(7))
		for j := 0; j < n; j++ {
			d.Transact(nil, func(tx *Tx) { tx.Insert(t, rng.Intn(tx.Len(t)+1), "a") })
		}
	}
}

func BenchmarkSequentialDelete(b *testing.B) {
	const n = benchRunes / 10
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		d := NewDocWith(Options{ClientID: 1})
		t := d.Text("body")
		d.Transact(nil, func(tx *Tx) { tx.Insert(t, 0, strings.Repeat("a", n)) })
		b.StartTimer()
		for j := 0; j < n; j++ {
			d.Transact(nil, func(tx *Tx) { tx.Delete(t, 0, 1) })
		}
	}
}

// benchSplitUpdate builds an update whose second client anchors inside the
// first client's single run, so integrating it has to split that run.
func benchSplitUpdate(b *testing.B, runes, splits int) Update {
	b.Helper()
	a := NewDocWith(Options{ClientID: 1})
	at := a.Text("body")
	a.Transact(nil, func(tx *Tx) { tx.Insert(at, 0, strings.Repeat("a", runes)) })
	c := NewDocWith(Options{ClientID: 2})
	ct := c.Text("body")
	if err := c.ApplyUpdate(a.EncodeStateAsUpdate(StateVector{}), nil); err != nil {
		b.Fatal(err)
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < splits; i++ {
		c.Transact(nil, func(tx *Tx) { tx.Insert(ct, rng.Intn(tx.Len(ct)+1), "b") })
	}
	return c.EncodeStateAsUpdate(StateVector{})
}

func BenchmarkLoadUpdate(b *testing.B) {
	u := benchSplitUpdate(b, benchRunes, 1000)
	b.SetBytes(int64(len(u)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := NewDocWith(Options{ClientID: 3})
		if err := d.ApplyUpdate(u, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// benchDivergent returns a shared base and the two updates a pair of replicas
// produce from it without ever seeing each other.
func benchDivergent(b *testing.B, runes, edits int) (base, ua, ub Update) {
	b.Helper()
	root := NewDocWith(Options{ClientID: 1})
	rt := root.Text("body")
	root.Transact(nil, func(tx *Tx) { tx.Insert(rt, 0, strings.Repeat("a", runes)) })
	base = root.EncodeStateAsUpdate(StateVector{})
	sv := root.StateVector()
	edit := func(id ClientID, seed int64) Update {
		d := NewDocWith(Options{ClientID: id})
		t := d.Text("body")
		if err := d.ApplyUpdate(base, nil); err != nil {
			b.Fatal(err)
		}
		rng := rand.New(rand.NewSource(seed))
		for i := 0; i < edits; i++ {
			d.Transact(nil, func(tx *Tx) { tx.Insert(t, rng.Intn(tx.Len(t)+1), "b") })
		}
		return d.EncodeStateAsUpdate(sv)
	}
	return base, edit(2, 7), edit(3, 11)
}

func BenchmarkConcurrentMerge(b *testing.B) {
	base, ua, ub := benchDivergent(b, benchRunes/10, 1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := NewDocWith(Options{ClientID: 4})
		for _, u := range []Update{base, ua, ub} {
			if err := d.ApplyUpdate(u, nil); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkString1MB(b *testing.B) {
	_, t := benchDoc(1 << 20)
	b.SetBytes(int64(len(t.String())))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchString = t.String()
	}
}

func BenchmarkEncodeStateAsUpdate1MB(b *testing.B) {
	d, _ := benchDoc(1 << 20)
	b.SetBytes(int64(len(d.EncodeStateAsUpdate(StateVector{}))))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchUpdate = d.EncodeStateAsUpdate(StateVector{})
	}
}

func TestEmptyTransactAllocatesNothing(t *testing.T) {
	if debugBuild {
		t.Skip("the debug tag allocates to read the goroutine id")
	}
	d := NewDocWith(Options{ClientID: 1})
	body := d.Text("body")
	d.Transact(nil, func(tx *Tx) { tx.Insert(body, 0, "seed") })
	if n := testing.AllocsPerRun(100, func() { d.Transact(nil, func(*Tx) {}) }); n != 0 {
		t.Fatalf("an empty transaction allocated %v times, want 0", n)
	}
}

// appendAllocBudget is a fence against a regression, not a target. The steady
// state is nine: the item, the encoded update, the delta and the bookkeeping
// around them.
const appendAllocBudget = 12

func TestSteadyStateAppendStaysInBudget(t *testing.T) {
	if debugBuild {
		t.Skip("the debug tag allocates to read the goroutine id")
	}
	d := NewDocWith(Options{ClientID: 1})
	body := d.Text("body")
	n := testing.AllocsPerRun(200, func() {
		d.Transact(nil, func(tx *Tx) { tx.Insert(body, tx.Len(body), "x") })
	})
	if n > appendAllocBudget {
		t.Fatalf("appending one rune allocated %v times, budget is %d", n, appendAllocBudget)
	}
}
