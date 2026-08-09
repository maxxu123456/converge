package converge

import (
	"slices"
	"sort"
	"strconv"
)

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

// structStore indexes every item this replica holds by (client, clock).
type structStore struct {
	clients map[ClientID]*clientBlocks
}

func (s *structStore) locate(id ID) (cb *clientBlocks, idx int, ok bool) {
	cb = s.clients[id.Client]
	if cb == nil {
		return nil, 0, false
	}
	idx, ok = cb.find(id.Clock)
	return cb, idx, ok
}

// get returns the block containing id.Clock, or nil. It never splits.
func (s *structStore) get(id ID) *item {
	cb, i, ok := s.locate(id)
	if !ok {
		return nil
	}
	return cb.blocks[i]
}

// nextBlock returns the next block of the same client by clock, or nil.
func (s *structStore) nextBlock(it *item) *item {
	cb, i, ok := s.locate(it.id)
	if !ok || i+1 == len(cb.blocks) {
		return nil
	}
	return cb.blocks[i+1]
}

// stateOf returns the next clock expected from c.
func (s *structStore) stateOf(c ClientID) uint64 {
	if cb := s.clients[c]; cb != nil {
		return cb.next
	}
	return 0
}

// stateVector returns the next expected clock for every client held.
func (s *structStore) stateVector() map[ClientID]uint64 {
	sv := make(map[ClientID]uint64, len(s.clients))
	for c, cb := range s.clients {
		// clock 0 is indistinguishable from absent, so it names nobody
		if cb.next != 0 {
			sv[c] = cb.next
		}
	}
	return sv
}

// add appends it to its client's blocks and panics on a gap or an overlap,
// which would break every lookup that assumes contiguous coverage.
func (s *structStore) add(it *item) {
	if s.clients == nil {
		s.clients = make(map[ClientID]*clientBlocks)
	}
	cb := s.clients[it.id.Client]
	if cb == nil {
		cb = new(clientBlocks)
		s.clients[it.id.Client] = cb
	}
	if it.id.Clock != cb.next {
		panic("converge: store.add: clock " + strconv.FormatUint(it.id.Clock, 10) +
			" does not continue " + strconv.FormatUint(cb.next, 10))
	}
	cb.blocks = append(cb.blocks, it)
	cb.next = it.endClock()
}

// splitAt cuts it at rune offset off and returns the new right half. Both
// halves keep the original deleted flag, so a split changes no visible length.
func (s *structStore) splitAt(it *item, off uint32) *item {
	if off == 0 || off >= it.runeLen {
		panic("converge: store.splitAt: offset " + strconv.FormatUint(uint64(off), 10) +
			" outside a run of " + strconv.FormatUint(uint64(it.runeLen), 10))
	}
	cb, i, ok := s.locate(it.id)
	if !ok || cb.blocks[i] != it {
		panic("converge: store.splitAt: item is not in the store")
	}
	byteOff := utf8ByteOffset(it.content, off)
	right := &item{
		id:          ID{it.id.Client, it.id.Clock + uint64(off)},
		origin:      ID{it.id.Client, it.id.Clock + uint64(off) - 1},
		rightOrigin: it.rightOrigin, // inherited, never recomputed
		left:        it,
		right:       it.right,
		parent:      it.parent,
		content:     it.content[byteOff:],
		runeLen:     it.runeLen - off,
		u16Len:      it.u16Len - utf16LenOf(it.content[:byteOff]),
		deleted:     it.deleted,
	}
	it.content = it.content[:byteOff]
	it.runeLen = off
	it.u16Len -= right.u16Len
	if right.right != nil {
		right.right.left = right
	}
	it.right = right
	cb.blocks = slices.Insert(cb.blocks, i+1, right)
	return right
}

// cleanStart returns the block that now starts at id.Clock, splitting the
// containing block when it starts earlier. nil when the store lacks id.
func (s *structStore) cleanStart(id ID) *item {
	it := s.get(id)
	if it == nil || it.id.Clock == id.Clock {
		return it
	}
	return s.splitAt(it, uint32(id.Clock-it.id.Clock))
}

// cleanEnd returns the block that now ends at id.Clock, splitting the
// containing block when it runs on. nil when the store lacks id.
func (s *structStore) cleanEnd(id ID) *item {
	it := s.get(id)
	if it == nil || it.endClock() == id.Clock+1 {
		return it
	}
	s.splitAt(it, uint32(id.Clock-it.id.Clock+1))
	return it
}
