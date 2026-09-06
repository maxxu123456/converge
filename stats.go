package converge

// Stats is a snapshot of a Doc's memory profile. Reading it is O(items).
type Stats struct {
	Clients        int // distinct ClientIDs in the struct store
	Items          int // runs currently in the store, live and tombstoned
	VisibleRunes   int // runes currently visible across every Text
	TombstoneRunes int // runes that were deleted and are retained forever
	ContentBytes   int // UTF-8 bytes of all item content, live and tombstoned
	PendingStructs int // same as the n returned by Doc.Pending
}

// Stats returns a snapshot of this document's memory profile.
func (d *Doc) Stats() Stats {
	d.lock()
	defer d.mu.Unlock()
	s := Stats{Clients: len(d.store.clients), PendingStructs: d.pendingCount}
	for _, cb := range d.store.clients {
		s.Items += len(cb.blocks)
		for _, it := range cb.blocks {
			s.ContentBytes += len(it.content)
			if it.deleted {
				s.TombstoneRunes += int(it.runeLen)
			} else {
				s.VisibleRunes += int(it.runeLen)
			}
		}
	}
	return s
}
