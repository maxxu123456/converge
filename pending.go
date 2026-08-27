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
