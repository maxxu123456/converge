package converge

import (
	"cmp"
	"slices"
)

// idRange covers the clocks [clock, clock+length) of one client.
type idRange struct {
	clock  uint64
	length uint64
}

func (r idRange) end() uint64 { return r.clock + r.length }

// deleteSet is the tombstoned clock ranges of each client. Canonical form is
// sorted, non-overlapping and non-adjacent, which normalize restores.
type deleteSet map[ClientID][]idRange

func (ds deleteSet) add(c ClientID, clock, length uint64) {
	if length == 0 {
		return
	}
	rs := ds[c]
	// deletes usually walk left to right, so the common case extends the last range
	if n := len(rs); n > 0 && rs[n-1].end() == clock {
		rs[n-1].length += length
		return
	}
	ds[c] = append(rs, idRange{clock, length})
}

// normalize sorts each client's ranges and coalesces the ones that overlap or
// touch, so the set is a function of the state and not of arrival order.
func (ds deleteSet) normalize() {
	for c, rs := range ds {
		if len(rs) == 0 {
			delete(ds, c)
			continue
		}
		slices.SortFunc(rs, func(a, b idRange) int { return cmp.Compare(a.clock, b.clock) })
		out := rs[:1]
		for _, r := range rs[1:] {
			last := &out[len(out)-1]
			if r.clock <= last.end() {
				if e := r.end(); e > last.end() {
					last.length = e - last.clock
				}
				continue
			}
			out = append(out, r)
		}
		ds[c] = out
	}
}

// covers reports whether [id.Clock, id.Clock+n) lies inside a single range.
func (ds deleteSet) covers(id ID, n uint32) bool {
	end := id.Clock + uint64(n)
	for _, r := range ds[id.Client] {
		if r.clock <= id.Clock && end <= r.end() {
			return true
		}
	}
	return false
}

// union folds other into ds and leaves ds canonical.
func (ds deleteSet) union(other deleteSet) {
	for c, rs := range other {
		ds[c] = append(ds[c], rs...)
	}
	ds.normalize()
}

func (ds deleteSet) empty() bool {
	for _, rs := range ds {
		if len(rs) > 0 {
			return false
		}
	}
	return true
}

// deleteSetFromStore rebuilds the complete delete set by walking the store.
// item.deleted is the only source of truth, so nothing can drift out of sync.
func deleteSetFromStore(st *structStore) deleteSet {
	ds := deleteSet{}
	for c, cb := range st.clients {
		for _, it := range cb.blocks {
			if it.deleted {
				ds.add(c, it.id.Clock, uint64(it.runeLen))
			}
		}
	}
	ds.normalize()
	return ds
}

// deleteItem tombstones it. This is the only place deleted is ever set, and it
// returns early on a tombstone, so the accounting cannot drift.
func deleteItem(tx *Tx, it *item) {
	if it.deleted {
		return
	}
	it.deleted = true
	t := it.parent
	t.runeLen -= int(it.runeLen)
	t.byteLen -= len(it.content)
	t.u16Len -= int(it.u16Len)
	tx.deleted.add(it.id.Client, it.id.Clock, uint64(it.runeLen))
	tx.merge = append(tx.merge, it)
}

// applyDeleteSet tombstones every range of ds this replica can resolve and
// returns what it could not. It runs after the structs, since a delete usually
// names text carried in the same update.
func applyDeleteSet(tx *Tx, ds deleteSet) deleteSet {
	unapplied := deleteSet{}
	for c, rs := range ds {
		for _, r := range rs {
			if tail := applyDeleteRange(tx, c, r.clock, r.end()); tail != nil {
				unapplied.add(c, tail.clock, tail.length)
			}
		}
	}
	unapplied.normalize()
	return unapplied
}

// applyDeleteRange tombstones the clocks [clock, end) of c that are held here
// and returns the tail it could not reach. Holding the whole range back would
// delay text that was perfectly deletable.
func applyDeleteRange(tx *Tx, c ClientID, clock, end uint64) *idRange {
	st := &tx.doc.store
	state := st.stateOf(c)
	if clock >= state {
		return &idRange{clock, end - clock}
	}
	stop := min(end, state)
	// split at both range boundaries before marking, or a run only partly
	// covered takes visible text down with it
	for it := st.cleanStart(ID{Client: c, Clock: clock}); it != nil && it.id.Clock < stop; {
		if it.endClock() > stop {
			st.splitAt(it, uint32(stop-it.id.Clock))
		}
		deleteItem(tx, it)
		// splitAt reallocates the client's slice, so ask the store again
		it = st.nextBlock(it)
	}
	if end > state {
		return &idRange{state, end - state}
	}
	return nil
}
