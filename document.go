package converge

import "sync"

// Doc is a CRDT document: named Text values sharing one replica identity and
// one causal history. Safe for concurrent use, but never copy one.
type Doc struct {
	mu sync.Mutex // guards everything below, never held across io or user code

	clientID ClientID
	roots    map[string]*Text
	store    structStore

	tx     Tx // the single reusable transaction value
	txOpen bool

	updObs    []*observer[func(Update, any)]
	nextObsID uint64
}

// Options configures a Doc. The zero Options is the default.
type Options struct {
	// ClientID pins this replica's identity instead of minting one from
	// crypto/rand. Only safe when the caller guarantees global uniqueness.
	ClientID ClientID
}

// NewDoc returns an empty Doc with a fresh random ClientID.
func NewDoc() *Doc { return NewDocWith(Options{}) }

// NewDocWith returns an empty Doc configured by opts.
func NewDocWith(opts Options) *Doc {
	c := opts.ClientID
	if c == 0 {
		c = newClientID()
	}
	return &Doc{clientID: c, roots: make(map[string]*Text)}
}

// ClientID returns this replica's identity. Use it to skip drawing your own
// remote cursor.
func (d *Doc) ClientID() ClientID {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.clientID
}

// Text returns the root Text named name, creating it on first use. It must not
// be called from inside a Transact callback, use Tx.Text.
func (d *Doc) Text(name string) *Text {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.text(name)
}

// text returns the root named name. The caller holds the lock.
func (d *Doc) text(name string) *Text {
	checkTextName(name)
	t := d.roots[name]
	if t == nil {
		t = &Text{doc: d, name: name}
		d.roots[name] = t
	}
	return t
}

// StateVector returns what this replica has seen: per client, the next clock
// it expects. The result is a value safe to hold and to send.
func (d *Doc) StateVector() StateVector {
	d.mu.Lock()
	defer d.mu.Unlock()
	return StateVector{m: d.store.stateVector()}
}

// EncodeStateAsUpdate returns everything this replica holds that the holder of
// since does not. The zero StateVector means everything.
func (d *Doc) EncodeStateAsUpdate(since StateVector) Update {
	d.mu.Lock()
	defer d.mu.Unlock()
	// the delete set goes out whole whatever since says: tombstoning advances
	// no clock, so a filtered one resurrects deleted text on the receiver
	return encodeCanonical(structsSince(&d.store, since.m), deleteSetFromStore(&d.store))
}

// ApplyUpdate integrates u, which may repeat what this replica already holds.
// A struct whose anchors are missing is dropped, so deliver in causal order.
func (d *Doc) ApplyUpdate(u Update, origin any) error {
	if len(u) == 0 {
		return nil
	}
	// decode and validate with no lock held and nothing mutated
	structs, ds, err := decodeUpdate(u)
	if err != nil {
		return err
	}
	if err := validateUpdate(structs); err != nil {
		return err
	}
	d.mu.Lock()
	tx := d.begin(origin)
	rejected := drive(tx, structs)
	applyDeleteSet(tx, ds)
	d.commit(tx)
	if rejected {
		return badUpdate(0, "struct.originOrder")
	}
	return nil
}

// applyDeleteSet tombstones every range of ds this replica can resolve. It runs
// after the structs, since a delete usually names text carried in the same update.
func applyDeleteSet(tx *Tx, ds deleteSet) {
	for c, rs := range ds {
		for _, r := range rs {
			applyDeleteRange(tx, c, r.clock, r.end())
		}
	}
}

// applyDeleteRange tombstones the clocks [clock, end) of c that are held here.
func applyDeleteRange(tx *Tx, c ClientID, clock, end uint64) {
	st := &tx.doc.store
	stop := min(end, st.stateOf(c))
	if clock >= stop {
		return
	}
	// split at both range boundaries before marking, or a run only partly
	// covered takes visible text down with it
	for it := st.cleanStart(ID{Client: c, Clock: clock}); it != nil && it.id.Clock < stop; {
		if it.endClock() > stop {
			st.splitAt(it, uint32(stop-it.id.Clock))
		}
		deleteItem(tx, it)
		// splitAt reallocates the client's slice, so ask the store again
		it = st.nextBlock(it)
	}
}

// OnUpdate registers fn, called once per committed transaction that changed the
// document. The returned func unregisters fn and is idempotent.
func (d *Doc) OnUpdate(fn func(u Update, origin any)) (cancel func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return addObserver(d, &d.updObs, fn)
}

// Transact runs fn as one atomic change. fn must not call any method on the Doc
// or on a Text: the document lock is held for the whole callback.
func (d *Doc) Transact(origin any, fn func(tx *Tx)) {
	d.mu.Lock()
	tx := d.begin(origin)
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
