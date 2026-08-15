package converge

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
