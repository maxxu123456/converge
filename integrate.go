package converge

import "errors"

// errParentMismatch is raised when a struct's two anchors sit in different
// roots, which no correct replica can produce.
var errParentMismatch = errors.New("converge: origins name different roots")

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

// integrate splices it into the list between it.left and it.right and updates
// its parent's visible counters.
func integrate(tx *Tx, it *item) {
	parent := it.parent
	// read left.right before overwriting it
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
}
