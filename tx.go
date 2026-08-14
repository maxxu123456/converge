package converge

// Tx is the sole mutation surface, and the only legal way to read a Text while
// a transaction is open. It is valid only inside the Transact callback.
type Tx struct {
	doc    *Doc
	origin any
	closed bool // set once the callback has returned
}

// begin opens the document's single reusable transaction. The caller holds the lock.
func (d *Doc) begin(origin any) *Tx {
	if d.txOpen {
		panic("converge: transaction already open")
	}
	d.tx = Tx{doc: d, origin: origin}
	d.txOpen = true
	return &d.tx
}

// commit closes the transaction and releases the document lock.
func (d *Doc) commit(tx *Tx) {
	tx.closed = true
	d.txOpen = false
	d.mu.Unlock()
}

// Doc returns the Doc this transaction belongs to.
func (tx *Tx) Doc() *Doc { return tx.doc }

// Origin returns the value passed to Transact.
func (tx *Tx) Origin() any { return tx.origin }

// Text returns the root Text named name, creating it on first use. This is the
// only legal way to reach a Text while a transaction is open.
func (tx *Tx) Text(name string) *Text { return tx.doc.text(name) }

// Len returns t's visible length in runes, as of this point in the transaction.
func (tx *Tx) Len(t *Text) int { return t.runeLen }

// String returns t's visible text, as of this point in the transaction.
func (tx *Tx) String(t *Text) string { return t.visible() }

// Slice returns the runes of t in [start, end), as of this point in the
// transaction.
func (tx *Tx) Slice(t *Text, start, end int) string { return t.visibleSlice(start, end) }
