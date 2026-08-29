package converge

import (
	"cmp"
	"slices"
)

// bufferStructs parks the structs a transaction could not place, so a later
// arrival unblocks them instead of the text being lost.
func (d *Doc) bufferStructs(leftover map[ClientID][]decoded) {
	for _, c := range sortedClients(leftover) {
		for _, s := range leftover[c] {
			if s.endClock() <= d.store.stateOf(c) {
				continue // already integrated, so there is nothing to wait for
			}
			d.pending[c] = append(d.pending[c], s)
			d.pendingCount++
		}
		d.tidyPending(c)
	}
}

// tidyPending keeps a client's buffer clock ascending and free of the repeats a
// blocked update delivered twice leaves behind.
func (d *Doc) tidyPending(c ClientID) {
	ss := d.pending[c]
	if len(ss) < 2 {
		return
	}
	slices.SortStableFunc(ss, func(a, b decoded) int { return cmp.Compare(a.clock, b.clock) })
	kept := slices.CompactFunc(ss, func(a, b decoded) bool {
		return a.clock == b.clock && a.runeLen == b.runeLen
	})
	d.pendingCount -= len(ss) - len(kept)
	d.pending[c] = kept
}

// drainPending retries the buffered structs until a round integrates nothing.
// It runs inside the transaction that unblocked them, so a whole cascade still
// emits one delta per Text and one relayed update.
func drainPending(tx *Tx) {
	d := tx.doc
	st := &d.store
	for progress := true; progress; {
		progress = false
		for _, c := range sortedClients(d.pending) {
			list := d.pending[c]
			i := 0
			for i < len(list) {
				s := list[i]
				state := st.stateOf(c)
				if s.endClock() <= state { // some other delivery got there first
					i++
					d.pendingCount--
					progress = true
					continue
				}
				// a later struct of c starts higher still, so it is blocked too
				if s.clock > state || !originsPresent(st, s) {
					break
				}
				it, err := materialize(tx, s, uint32(state-s.clock))
				if err == nil {
					err = integrate(tx, it)
				}
				if err != nil {
					// the rest of c is one clock chain that can never complete
					d.pendingCount -= len(list) - i
					i = len(list)
					break
				}
				i++
				d.pendingCount--
				progress = true
			}
			if list = list[i:]; len(list) == 0 {
				delete(d.pending, c)
			} else {
				d.pending[c] = list
			}
		}
	}
}

// bufferDeleteSet parks the delete ranges naming structs this replica does not
// hold, then retries the backlog in case this update brought what it needed.
func (d *Doc) bufferDeleteSet(tx *Tx, unapplied deleteSet) {
	if unapplied.empty() {
		return
	}
	d.pendingDS.union(unapplied)
	retryPendingDeleteSet(tx)
}

// retryPendingDeleteSet reapplies every buffered range and keeps whatever is
// still out of reach. The ranges never pre-mark an item, so nothing here can
// tombstone text the delete did not name.
func retryPendingDeleteSet(tx *Tx) {
	d := tx.doc
	if len(d.pendingDS) == 0 {
		return
	}
	kept := deleteSet{}
	for c, rs := range d.pendingDS {
		for _, r := range rs {
			if tail := applyDeleteRange(tx, c, r.clock, r.end()); tail != nil {
				kept.add(c, tail.clock, tail.length)
			}
		}
	}
	kept.normalize()
	d.pendingDS = kept
}
