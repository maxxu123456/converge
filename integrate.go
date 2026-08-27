package converge

import (
	"cmp"
	"errors"
	"slices"
)

// errParentMismatch is raised when a struct's two anchors sit in different
// roots, which no correct replica can produce.
var errParentMismatch = errors.New("converge: origins name different roots")

// errOriginOrder is raised when a struct's origin does not lie to the left of
// its rightOrigin in this replica's list.
var errOriginOrder = errors.New("converge: origins out of order")

// materialize turns a decoded struct into an item and resolves its anchors
// against this replica's list. offset trims a prefix we already hold.
func materialize(tx *Tx, s decoded, offset uint32) (*item, error) {
	if offset > 0 {
		// two peers may split one run at different offsets, so an incoming
		// struct often repeats runes we hold and carries a tail we do not
		b := utf8ByteOffset(s.content, offset)
		s.clock += uint64(offset)
		s.origin = ID{Client: s.client, Clock: s.clock - 1}
		s.content = s.content[b:]
		s.runeLen -= offset
		s.u16Len = utf16LenOf(s.content)
	}
	it := &item{
		id:          ID{Client: s.client, Clock: s.clock},
		origin:      s.origin,
		rightOrigin: s.rightOrigin,
		content:     s.content,
		runeLen:     s.runeLen,
		u16Len:      s.u16Len,
	}
	st := &tx.doc.store
	// the scan compares anchor ids for exact equality, so an anchor naming a
	// clock mid-run has to become a boundary before the scan can use it
	if !it.origin.IsZero() {
		it.left = st.cleanEnd(it.origin)
		it.parent = it.left.parent
	}
	// origin first: if the anchors have since merged into one run, splitting
	// there leaves the right half already starting at rightOrigin
	if !it.rightOrigin.IsZero() {
		it.right = st.cleanStart(it.rightOrigin)
		if it.parent != nil && it.parent != it.right.parent {
			return nil, errParentMismatch
		}
		it.parent = it.right.parent
	}
	if it.parent == nil {
		it.parent = tx.doc.text(s.parentName)
	}
	return it, nil
}

// drive integrates every struct whose dependencies this replica already holds,
// cycling per-client cursors until a whole round makes no progress. What it
// leaves over is causally blocked, not junk.
func drive(tx *Tx, structs map[ClientID][]decoded) (leftover map[ClientID][]decoded, rejected bool) {
	st := &tx.doc.store
	order := sortedClients(structs)
	cursor := make(map[ClientID]int, len(structs))
	dead := make(map[ClientID]bool)
	for {
		progress := false
		for _, c := range order {
			if dead[c] {
				continue
			}
			ss, i := structs[c], cursor[c]
			// strictly by clock, so a struct never lands before its own predecessor
			for i < len(ss) {
				s := ss[i]
				state := st.stateOf(c)
				if s.endClock() <= state { // a duplicate delivery, already held
					i++
					progress = true
					continue
				}
				if s.clock > state { // a gap in this client's own log
					break
				}
				if !originsPresent(st, s) {
					break
				}
				it, err := materialize(tx, s, uint32(state-s.clock))
				if err == nil {
					err = integrate(tx, it)
				}
				if err != nil {
					// the rest of c is one clock chain that can never complete
					dead[c], rejected = true, true
					break
				}
				i++
				progress = true
			}
			cursor[c] = i
		}
		if !progress {
			break
		}
	}
	for _, c := range order {
		if tail := structs[c][cursor[c]:]; !dead[c] && len(tail) > 0 {
			if leftover == nil {
				leftover = make(map[ClientID][]decoded, len(order))
			}
			leftover[c] = tail
		}
	}
	return leftover, rejected
}

// originsPresent reports whether both of s's anchors are already in the store.
func originsPresent(st *structStore, s decoded) bool {
	if !s.origin.IsZero() && st.stateOf(s.origin.Client) <= s.origin.Clock {
		return false
	}
	if !s.rightOrigin.IsZero() && st.stateOf(s.rightOrigin.Client) <= s.rightOrigin.Clock {
		return false
	}
	// a same-client origin needs no guard: it is below s.clock, which is held
	return true
}

// integrate splices it into the list, scanning conflicts in YATA order, and
// updates its parent's visible counters.
func integrate(tx *Tx, it *item) error {
	parent := it.parent
	left, right := it.left, it.right
	// did anything land in the gap since it was created? inverting this guard
	// is a divergence, skipping it is only slow
	contested := (left == nil && (right == nil || right.left != nil)) ||
		(left != nil && left.right != right)

	if contested {
		o := parent.start
		if left != nil {
			o = left.right
		}
		// these are compared by pointer: keyed by id, before would hold a run's
		// first id while the lookup asks for its last, which kills case 2
		var before, conf []*item // scanned so far, and scanned since left moved
	scan:
		for o != nil && o != right {
			before = append(before, o)
			conf = append(conf, o)
			switch {
			case o.origin == it.origin:
				// o and it claim one insertion point, so break the tie on
				// ClientID, the only totally ordered replica-free value there is
				if o.id.Client < it.id.Client {
					left = o
					conf = conf[:0] // everything up to o is settled
				} else if it.rightOrigin == o.rightOrigin {
					break scan // the same interval, and we lost: o is our right neighbour
				}
				// o reaches further right than we do, so its own subtree may
				// still hold items that have to precede us: keep scanning
			case !o.origin.IsZero():
				oo := tx.doc.store.get(o.origin) // the run holding that clock, no split
				if !slices.Contains(before, oo) {
					break scan // o hangs off something outside our window
				}
				if !slices.Contains(conf, oo) {
					// o descends from an item we already sit to the right of
					left = o
					conf = conf[:0]
				}
			default:
				break scan // o has no origin and is not our conflict peer
			}
			o = o.right
		}
		// walking off the end with a right anchor means the origin does not lie
		// left of the rightOrigin here, which no correct replica produces
		if o == nil && right != nil {
			return errOriginOrder
		}
		it.left = left
	}

	// read left.right before overwriting it, and recompute right from the final
	// left: the rightOrigin only ever served as the scan terminator
	if it.left != nil {
		it.right = it.left.right
		it.left.right = it
	} else {
		it.right = parent.start
		parent.start = it
	}
	if it.right != nil {
		it.right.left = it
	}
	tx.doc.store.add(it)
	if !it.deleted {
		parent.runeLen += int(it.runeLen)
		parent.byteLen += len(it.content)
		parent.u16Len += int(it.u16Len)
	}
	tx.merge = append(tx.merge, it)
	return nil
}

// mergePass folds what this transaction changed into the runs around it, and
// repeats until a round folds nothing, since one fold exposes the next pair.
func mergePass(tx *Tx) {
	st := &tx.doc.store
	slices.SortFunc(tx.merge, func(a, b *item) int {
		if a.id.Client != b.id.Client {
			return cmp.Compare(a.id.Client, b.id.Client)
		}
		return cmp.Compare(a.id.Clock, b.id.Clock)
	})
	for progress := true; progress; {
		progress = false
		for _, it := range tx.merge {
			if st.get(it.id) != it { // already folded into its neighbour
				continue
			}
			if tryMergeLeft(st, it) {
				progress = true
			}
			// whatever followed it is now list-adjacent to the run that
			// absorbed it, and is not always a candidate of its own
			if r := it.right; r != nil && tryMergeLeft(st, r) {
				progress = true
			}
		}
	}
}
