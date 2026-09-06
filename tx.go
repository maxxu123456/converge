package converge

import (
	"slices"
	"unicode/utf8"
)

// Tx is the sole mutation surface, and the only legal way to read a Text while
// a transaction is open. It is valid only inside the Transact callback.
type Tx struct {
	doc     *Doc
	origin  any
	local   bool                // a Transact, rather than an ApplyUpdate
	closed  bool                // set once the callback has returned
	before  map[ClientID]uint64 // the store's state vector at begin
	deleted deleteSet           // runes this transaction turned into tombstones
	merge   []*item             // fold candidates: integrated here, or tombstoned here
}

// Transact runs fn as one atomic change. fn must not call any method on the Doc
// or on a Text: the document lock is held for the whole callback. A panic from
// fn commits what it had already applied and then propagates.
func (d *Doc) Transact(origin any, fn func(tx *Tx)) {
	d.mu.Lock()
	d.noteTxEnter()
	defer d.noteTxExit()
	tx := d.begin(origin, true)
	committed := false
	// what fn already applied is in the list, so a panicking fn still commits
	defer func() {
		if !committed {
			d.commit(tx)
		}
	}()
	fn(tx)
	committed = true
	d.commit(tx)
}

// begin opens the document's single reusable transaction. The caller holds the lock.
func (d *Doc) begin(origin any, local bool) *Tx {
	if d.txOpen {
		panic("converge: transaction already open")
	}
	tx := &d.tx
	if tx.before == nil {
		tx.before, tx.deleted = map[ClientID]uint64{}, deleteSet{}
	}
	// the bookkeeping outlives the transaction, so a binding that applies an
	// empty delta pays nothing for the round trip
	clear(tx.before)
	clear(tx.deleted)
	tx.doc, tx.origin, tx.local, tx.closed, tx.merge = d, origin, local, false, tx.merge[:0]
	d.store.readStateVector(tx.before)
	d.txOpen = true
	return tx
}

// commit closes the transaction, releases the document lock and delivers what
// changed to the observers with the lock down.
func (d *Doc) commit(tx *Tx) {
	// a local edit is as likely to unblock a buffered struct as a remote one,
	// so the retry belongs at the end of every transaction
	drainPending(tx)
	// and the delete set with it: an update that carries the text a buffered
	// range names buffers nothing itself, so nothing else would ever retry it
	retryPendingDeleteSet(tx)
	d.recomputeMissing()
	tx.deleted.normalize()
	changed := !d.store.clocksMatch(tx.before) || !tx.deleted.empty()
	tx.closed = true
	d.txOpen = false
	if !changed {
		// a transaction that changed nothing emits nothing, which is what
		// stops a duplicate update from starting an echo storm
		d.mu.Unlock()
		return
	}
	upd := encodeCanonical(structsSince(&d.store, tx.before), tx.deleted)
	events := deltasFor(tx)
	// strictly last: folding a fresh keystroke into the run before it would put
	// its clock below tx.before, and the delta would call it text that was
	// always there
	mergePass(tx)
	d.queue = append(d.queue, notification{events: events, update: upd, origin: tx.origin})
	if d.delivering {
		// hand it to the goroutine already draining, which is what keeps
		// delivery in commit order without holding the mutex across user code
		d.mu.Unlock()
		return
	}
	d.delivering = true
	// by defer, so an observer that panics cannot wedge the document
	defer func() { d.delivering = false; d.mu.Unlock() }()
	for len(d.queue) > 0 {
		n := d.queue[0]
		d.queue = d.queue[1:]
		d.dispatch(n)
	}
}

// dispatch delivers one notification with the document lock released, and
// retakes the lock even when an observer panics.
func (d *Doc) dispatch(n notification) {
	// the lists are snapshotted here, so cancelling during dispatch is legal
	// and takes effect from the next notification
	tobs := make([][]*observer[func(Event)], len(n.events))
	for i, ev := range n.events {
		tobs[i] = slices.Clone(ev.Text.obs)
	}
	uobs := slices.Clone(d.updObs)
	prev := d.noteTxSuspend()
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.noteTxResume(prev) }()
	for i, ev := range n.events {
		for _, o := range tobs[i] {
			o.fn(ev)
		}
	}
	for _, o := range uobs {
		o.fn(n.update, n.origin)
	}
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
