package converge

import "sync"

// Doc is a CRDT document: named Text values sharing one replica identity and
// one causal history. Safe for concurrent use, but never copy one.
type Doc struct {
	mu sync.Mutex // guards everything below, never held across io or user code

	clientID ClientID
	roots    map[string]*Text
	store    structStore

	// structs whose anchors this replica does not hold yet
	pending      map[ClientID][]decoded
	pendingCount int

	tx     Tx // the single reusable transaction value
	txOpen bool

	queue      []notification
	delivering bool
	updObs     []*observer[func(Update, any)]
	nextObsID  uint64
}

// notification is one committed change waiting to be handed to the observers.
type notification struct {
	events []Event
	update Update
	origin any
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
	return &Doc{
		clientID: c,
		roots:    make(map[string]*Text),
		pending:  make(map[ClientID][]decoded),
	}
}

// ClientID returns this replica's identity. Use it to skip drawing your own
// remote cursor.
func (d *Doc) ClientID() ClientID {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.clientID
}

// Text returns the root Text named name, creating it on first use. name must be
// 1 to 255 bytes of valid UTF-8. Never call it from inside a Transact callback,
// use Tx.Text.
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
// A struct whose anchors are missing is buffered and retried, never dropped.
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
	tx := d.begin(origin, false)
	leftover, rejected := drive(tx, structs)
	applyDeleteSet(tx, ds)
	d.bufferStructs(leftover)
	d.commit(tx)
	if rejected {
		return badUpdate(0, "struct.originOrder")
	}
	return nil
}

// OnUpdate registers fn, called once per committed transaction that changed the
// document. The returned func unregisters fn and is idempotent.
func (d *Doc) OnUpdate(fn func(u Update, origin any)) (cancel func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return addObserver(d, &d.updObs, fn)
}
