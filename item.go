package converge

import "unicode/utf8"

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

// utf8ByteOffset returns the byte index of rune off in s. off is always within
// s, so the result is always a code point boundary.
func utf8ByteOffset(s string, off uint32) int {
	b := 0
	for i := uint32(0); i < off; i++ {
		_, n := utf8.DecodeRuneInString(s[b:])
		b += n
	}
	return b
}

func utf16LenOf(s string) uint32 {
	var n uint32
	for _, r := range s {
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}
