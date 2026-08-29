package converge

import (
	"bytes"
	"testing"
)

// FuzzDecodeUpdate asserts decode is total: any bytes at all either fail, or
// re-encode to exactly the bytes they came from.
func FuzzDecodeUpdate(f *testing.F) {
	for _, c := range acceptedUpdates {
		f.Add(c.in)
	}
	for _, c := range rejectedUpdates {
		f.Add(c.in)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		structs, ds, err := decodeUpdate(b)
		if err != nil {
			return
		}
		if got := encodeStructs(structs, ds); !bytes.Equal(got, b) {
			t.Fatalf("decoded %x and re-encoded it to %x", b, got)
		}
	})
}

// FuzzMergeUpdates asserts the merge is a function of the set of its inputs:
// one answer for every permutation, and remerging it changes nothing.
func FuzzMergeUpdates(f *testing.F) {
	addMergeSeeds(f)
	f.Fuzz(func(t *testing.T, a, b, c []byte) {
		us := []Update{a, b, c}
		want, err := MergeUpdates(us...)
		if err != nil {
			return
		}
		for _, order := range threeOrders {
			got, err := MergeUpdates(us[order[0]], us[order[1]], us[order[2]])
			if err != nil {
				t.Fatalf("order %v failed where the first order did not: %v", order, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("order %v merges to %x, want %x", order, got, want)
			}
		}
		again, err := MergeUpdates(want)
		if err != nil {
			t.Fatalf("a merge does not decode: %v", err)
		}
		if !bytes.Equal(again, want) {
			t.Fatalf("remerging %x gave %x", want, again)
		}
		// inputs that contradict each other have no shared document for the
		// merge to preserve, so there is nothing to compare it against
		if !inputsAgree(us) {
			return
		}
		merged := integrateToFixpoint(want)
		apart := integrateToFixpoint(us...)
		if !bytes.Equal(merged, apart) {
			t.Fatalf("the merge integrates to %x, its inputs to %x", merged, apart)
		}
	})
}

func addMergeSeeds(f *testing.F) {
	a := NewDocWith(Options{ClientID: 1})
	b := NewDocWith(Options{ClientID: 2})
	ta, tb := a.Text("body"), b.Text("body")
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 0, "hello") })
	base := a.EncodeStateAsUpdate(StateVector{})
	if err := b.ApplyUpdate(base, "peer"); err != nil {
		f.Fatalf("seed: %v", err)
	}
	b.Transact(nil, func(tx *Tx) { tx.Insert(tb, 5, " world") })
	a.Transact(nil, func(tx *Tx) { tx.Delete(ta, 0, 2) })
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 0, "HE") })

	first := []byte(base)
	whole := []byte(a.EncodeStateAsUpdate(StateVector{}))
	tail := []byte(b.EncodeStateAsUpdate(StateVector{m: map[ClientID]uint64{2: 1}}))
	f.Add(first, whole, []byte(b.EncodeStateAsUpdate(StateVector{})))
	f.Add(whole, tail, whole)
	f.Add(first, first, []byte(nil))
	f.Add([]byte(nil), []byte(nil), []byte(nil))
}

// integrateToFixpoint delivers us until the document stops changing. A replica
// with no buffer drops what it cannot place yet, so one pass is not enough.
func integrateToFixpoint(us ...Update) []byte {
	d := NewDocWith(Options{ClientID: 1})
	got := d.EncodeStateAsUpdate(StateVector{})
	for range len(us) + 1 {
		for _, u := range us {
			_ = d.ApplyUpdate(u, "peer")
		}
		next := d.EncodeStateAsUpdate(StateVector{})
		if bytes.Equal(got, next) {
			break
		}
		got = next
	}
	return got
}

// fact is what one input says about a single rune: the rune and the anchors a
// receiver would give it.
type fact struct {
	origin      ID
	rightOrigin ID
	parentName  string
	r           rune
}

// inputsAgree reports whether the updates describe the same rune, with the same
// anchors, at every clock more than one of them covers.
func inputsAgree(us []Update) bool {
	facts := make(map[ID]fact)
	for _, u := range us {
		structs, _, err := decodeUpdate(u)
		if err != nil {
			continue
		}
		for c, ss := range structs {
			for _, s := range ss {
				clock := s.clock
				for i, r := range []rune(s.content) {
					f := fact{origin: ID{Client: c, Clock: clock - 1}, rightOrigin: s.rightOrigin, r: r}
					if i == 0 {
						f.origin, f.parentName = s.origin, s.parentName
					}
					if old, seen := facts[ID{c, clock}]; seen && old != f {
						return false
					}
					facts[ID{c, clock}] = f
					clock++
				}
			}
		}
	}
	return true
}

// FuzzConverge reads the fuzzer's bytes as a program: the first byte sizes the
// mesh and the rest choose who edits, who delivers and who drops out. Coverage
// guidance reaches the conflict scan far more often than random choices do.
func FuzzConverge(f *testing.F) {
	f.Add([]byte{0, 3, 1, 8, 6, 0, 0, 7, 11, 2, 3, 1, 6, 6, 5})
	f.Add([]byte{2, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 9, 6, 3})
	f.Add([]byte{3, 11, 0, 0, 4, 2, 9, 1, 11, 1, 6, 0, 10, 3, 3, 8})
	f.Fuzz(func(t *testing.T, script []byte) {
		if len(script) < 2 {
			return
		}
		if len(script) > 512 {
			script = script[:512]
		}
		src := &scriptSource{b: script[1:]}
		w := newWorld(t, "script", src, 2+int(script[0])%4, []string{"body"})
		for !src.spent() {
			w.step()
		}
		w.settle()
		w.converged()
	})
}

// FuzzPosition drives two replicas from the script, then asserts a position
// taken on one survives the wire and lands inside both documents.
func FuzzPosition(f *testing.F) {
	f.Add([]byte{0, 3, 2, 5, 6, 1, 7, 2}, 2, int8(0))
	f.Add([]byte{1, 4, 0, 0, 5, 9, 4, 3, 6, 2}, 0, int8(-1))
	f.Add([]byte{2, 8, 3, 1, 7, 7, 1, 6}, 5, int8(3))
	f.Fuzz(func(t *testing.T, script []byte, index int, assoc int8) {
		a := NewDocWith(Options{ClientID: 1})
		b := NewDocWith(Options{ClientID: 2})
		docs := [2]*Doc{a, b}
		// hoisted: Doc.Text takes the lock a transaction already holds
		texts := [2]*Text{a.Text("body"), b.Text("body")}
		for i := 0; i+1 < len(script) && i < 64; i += 2 {
			k := int(script[i] & 1)
			d, text, arg := docs[k], texts[k], int(script[i+1])
			switch int(script[i]>>1) % 4 {
			case 0, 1:
				d.Transact(nil, func(tx *Tx) {
					tx.Insert(text, arg%(tx.Len(text)+1), simAlphabet[arg%len(simAlphabet)])
				})
			case 2:
				d.Transact(nil, func(tx *Tx) {
					if n := tx.Len(text); n > 0 {
						at := arg % n
						tx.Delete(text, at, 1+arg%(n-at))
					}
				})
			default:
				sendState(t, a, b)
				sendState(t, b, a)
			}
		}

		p := texts[0].Position(index, Assoc(assoc))
		raw, err := p.MarshalBinary()
		if err != nil {
			t.Fatalf("%+v did not marshal: %v", p, err)
		}
		var got Position
		if err := got.UnmarshalBinary(raw); err != nil {
			t.Fatalf("%+v encoded to %x, which does not decode: %v", p, raw, err)
		}
		if got != p {
			t.Fatalf("%+v round tripped to %+v", p, got)
		}
		for i, d := range docs {
			text, at, ok := d.Resolve(got)
			if !ok {
				continue
			}
			if at < 0 || at > text.Len() {
				t.Fatalf("replica %d resolved to index %d, outside [0, %d]", i, at, text.Len())
			}
		}
		validate(t, a)
		validate(t, b)
	})
}
