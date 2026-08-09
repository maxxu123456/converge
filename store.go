package converge

import "sort"

// clientBlocks holds every run one client ever produced. The slice is ordered
// by id.Clock and covers [0, next) with no gaps and no overlaps, which is what
// makes the state vector one number per client and find a binary search.
type clientBlocks struct {
	blocks []*item
	next   uint64 // endClock of the last block, 0 when empty
}

// find returns the index of the block containing clock, false once clock is at
// or past next.
func (cb *clientBlocks) find(clock uint64) (idx int, ok bool) {
	if clock >= cb.next {
		return 0, false
	}
	// coverage is gap-free, so the first block ending past clock is the one holding it
	return sort.Search(len(cb.blocks), func(i int) bool {
		return cb.blocks[i].endClock() > clock
	}), true
}
