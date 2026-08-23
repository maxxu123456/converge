package converge

import (
	"cmp"
	"slices"
)

// Update is an encoded set of document changes. Applying one is commutative,
// associative and idempotent, and a nil or empty Update is a valid no-op.
type Update []byte

// MergeUpdates folds updates into one canonical Update. The result depends on
// the set of inputs and never on their order.
func MergeUpdates(updates ...Update) (Update, error) {
	structs := make(map[ClientID][]decoded)
	ds := deleteSet{}
	for _, u := range updates {
		ss, uds, err := readUpdate(u)
		if err != nil {
			return nil, err
		}
		for c, run := range ss {
			structs[c] = append(structs[c], run...)
		}
		ds.union(uds)
	}
	for c, ss := range structs {
		structs[c] = cover(ss)
	}
	// two updates can each be sound and still name each other's structs as
	// origins, and only their union holds the cycle
	if err := validateUpdate(structs); err != nil {
		return nil, err
	}
	return encodeCanonical(structs, ds), nil
}

// StateVector reports, per client, the first clock u stops covering. That is
// what a receiver could integrate from u alone, gaps being unintegrable.
func (u Update) StateVector() (StateVector, error) {
	structs, _, err := readUpdate(u)
	if err != nil {
		return StateVector{}, err
	}
	m := make(map[ClientID]uint64, len(structs))
	for c, ss := range structs {
		var next uint64
		for _, s := range ss {
			if s.clock != next {
				break
			}
			next = s.endClock()
		}
		if next != 0 {
			m[c] = next
		}
	}
	return StateVector{m: m}, nil
}

// Diff strips from u every struct sv already covers. The delete set goes out
// whole, since tombstoning advances no clock for sv to have seen.
func (u Update) Diff(sv StateVector) (Update, error) {
	structs, ds, err := readUpdate(u)
	if err != nil {
		return nil, err
	}
	out := make(map[ClientID][]decoded, len(structs))
	for c, ss := range structs {
		from := sv.Get(c)
		kept := make([]decoded, 0, len(ss))
		for _, s := range ss {
			if s.endClock() > from {
				kept = append(kept, sliceStruct(s, from))
			}
		}
		out[c] = kept
	}
	return encodeCanonical(out, ds), nil
}

// readUpdate decodes and validates u, as ApplyUpdate does, so the functions
// over bytes alone reject exactly what a document would.
func readUpdate(u Update) (map[ClientID][]decoded, deleteSet, error) {
	if len(u) == 0 {
		return nil, nil, nil
	}
	structs, ds, err := decodeUpdate(u)
	if err != nil {
		return nil, nil, err
	}
	if err := validateUpdate(structs); err != nil {
		return nil, nil, err
	}
	return structs, ds, nil
}

// cover reduces one client's structs to a clock-ordered set covering each clock
// exactly once. Where two overlap they carry the same runes, so either serves.
func cover(ss []decoded) []decoded {
	slices.SortFunc(ss, compareStructs)
	out := make([]decoded, 0, len(ss))
	var next uint64
	for _, s := range ss {
		if s.endClock() <= next {
			continue
		}
		out = append(out, sliceStruct(s, next))
		next = s.endClock()
	}
	return out
}

// compareStructs is a total order, which is what makes cover a function of the
// set: two structs over one clock range must not be left in input order.
func compareStructs(a, b decoded) int {
	if a.clock != b.clock {
		return cmp.Compare(a.clock, b.clock)
	}
	if a.runeLen != b.runeLen {
		return cmp.Compare(b.runeLen, a.runeLen) // the wider struct first
	}
	if c := cmp.Compare(a.content, b.content); c != 0 {
		return c
	}
	if c := compareIDs(a.origin, b.origin); c != 0 {
		return c
	}
	if c := compareIDs(a.rightOrigin, b.rightOrigin); c != 0 {
		return c
	}
	return cmp.Compare(a.parentName, b.parentName)
}

func compareIDs(a, b ID) int {
	if a.Client != b.Client {
		return cmp.Compare(a.Client, b.Client)
	}
	return cmp.Compare(a.Clock, b.Clock)
}
