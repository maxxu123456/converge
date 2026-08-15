package converge

import "unicode/utf8"

// Tx is the sole mutation surface, and the only legal way to read a Text while
// a transaction is open. It is valid only inside the Transact callback.
type Tx struct {
	doc     *Doc
	origin  any
	closed  bool      // set once the callback has returned
	deleted deleteSet // runes this transaction turned into tombstones
}

// begin opens the document's single reusable transaction. The caller holds the lock.
func (d *Doc) begin(origin any) *Tx {
	if d.txOpen {
		panic("converge: transaction already open")
	}
	d.tx = Tx{doc: d, origin: origin, deleted: deleteSet{}}
	d.txOpen = true
	return &d.tx
}

// commit closes the transaction and releases the document lock.
func (d *Doc) commit(tx *Tx) {
	tx.deleted.normalize()
	tx.closed = true
	d.txOpen = false
	d.mu.Unlock()
}

// check panics unless t belongs to tx's Doc and tx is still open.
func (tx *Tx) check(t *Text) {
	if t.doc != tx.doc {
		panic(&UsageError{Msg: "Text belongs to a different Doc"})
	}
	tx.checkOpen()
}

func (tx *Tx) checkOpen() {
	if tx.closed {
		panic(&UsageError{Msg: "Tx used after its callback returned"})
	}
}

// Doc returns the Doc this transaction belongs to.
func (tx *Tx) Doc() *Doc { tx.checkOpen(); return tx.doc }

// Origin returns the value passed to Transact.
func (tx *Tx) Origin() any { tx.checkOpen(); return tx.origin }

// Text returns the root Text named name, creating it on first use. This is the
// only legal way to reach a Text while a transaction is open.
func (tx *Tx) Text(name string) *Text {
	tx.checkOpen()
	return tx.doc.text(name)
}

// Insert inserts s before the rune at index of t, where index may equal the
// visible length. An empty s is a no-op, an index out of range panics.
func (tx *Tx) Insert(t *Text, index int, s string) {
	tx.check(t)
	if !utf8.ValidString(s) {
		panic(&UsageError{Msg: "Insert: s is not valid UTF-8"})
	}
	if index < 0 || index > t.runeLen {
		panic(rangeErr(t, index, 0))
	}
	if s == "" {
		return
	}
	d := tx.doc
	left, right := t.findInsertPos(index)
	it := &item{
		// the clock comes from the store, never from a counter on Doc
		id:      ID{d.clientID, d.store.stateOf(d.clientID)},
		left:    left,
		right:   right,
		parent:  t,
		content: s,
		runeLen: uint32(utf8.RuneCountInString(s)),
		u16Len:  utf16LenOf(s),
	}
	if left != nil {
		it.origin = left.lastID()
	}
	if right != nil {
		it.rightOrigin = right.id // may name a tombstone, which is what keeps the intention
	}
	integrate(tx, it)
}

// Delete removes length runes starting at index of t. A length of zero or less
// is a no-op, a range reaching past the end panics.
func (tx *Tx) Delete(t *Text, index, length int) {
	tx.check(t)
	if length <= 0 {
		return
	}
	if index < 0 || index+length > t.runeLen {
		panic(rangeErr(t, index, length))
	}
	s := &tx.doc.store
	it, off := t.findVisible(index)
	if off > 0 {
		it = s.splitAt(it, uint32(off))
	}
	for remaining := length; remaining > 0 && it != nil; it = it.right {
		if it.deleted {
			continue // a tombstone consumes none of the length
		}
		if int(it.runeLen) > remaining {
			s.splitAt(it, uint32(remaining))
		}
		remaining -= int(it.runeLen)
		deleteItem(tx, it)
	}
}

// Len returns t's visible length in runes, as of this point in the transaction.
func (tx *Tx) Len(t *Text) int {
	tx.check(t)
	return t.runeLen
}

// String returns t's visible text, as of this point in the transaction.
func (tx *Tx) String(t *Text) string {
	tx.check(t)
	return t.visible()
}

// Slice returns t's runes in [start, end) as of this point in the transaction.
// Panics with *RangeError out of range.
func (tx *Tx) Slice(t *Text, start, end int) string {
	tx.check(t)
	checkSlice(t, start, end)
	return t.visibleSlice(start, end)
}
