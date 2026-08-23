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

// tryMergeLeft folds r into its left neighbour when the two are one run that a
// split could have produced. All six tests are load-bearing.
func tryMergeLeft(st *structStore, r *item) bool {
	l := r.left
	if l == nil || l.parent != r.parent || l.id.Client != r.id.Client {
		return false
	}
	if l.endClock() != r.id.Clock || l.right != r {
		return false
	}
	// the same insertion interval, or the fold erases that r's was the narrower
	// one and a later concurrent item picks the other side of it
	if r.origin != l.lastID() || l.rightOrigin != r.rightOrigin {
		return false
	}
	// folding across a tombstone boundary would put live runes in the delete set
	if l.deleted != r.deleted {
		return false
	}
	l.content += r.content
	l.runeLen += r.runeLen
	l.u16Len += r.u16Len
	l.right = r.right
	if r.right != nil {
		r.right.left = l
	}
	return true
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
}

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
