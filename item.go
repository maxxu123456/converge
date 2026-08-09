package converge

// item is one contiguous run of runes from one client that shared the same
// neighbours when it was inserted. It covers clocks [id.Clock, endClock()).
type item struct {
	id          ID
	origin      ID    // the rune immediately left at insert time, zero when none
	rightOrigin ID    // the rune immediately right at insert time, zero when none
	left        *item // current predecessor in the list, tombstones included
	right       *item // current successor in the list, tombstones included
	parent      *Text
	content     string // UTF-8, immutable, never empty
	runeLen     uint32 // also the run's clock length, always at least 1
	u16Len      uint32 // length in UTF-16 code units
	deleted     bool   // tombstone, never reclaimed
}

func (it *item) lastID() ID {
	return ID{it.id.Client, it.id.Clock + uint64(it.runeLen) - 1}
}

func (it *item) endClock() uint64 { return it.id.Clock + uint64(it.runeLen) }
