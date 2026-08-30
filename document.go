package converge

import (
	"maps"
	"sync"
)

// Doc is a CRDT document: named Text values sharing one replica identity and
// one causal history. Safe for concurrent use, but never copy one.
type Doc struct {
	mu sync.Mutex // guards everything below, never held across io or user code

	clientID   ClientID
	maxPending int
	roots      map[string]*Text
	store      structStore

	// structs whose anchors this replica does not hold yet, delete ranges
	// naming structs it does not hold either, and what it is waiting for
	pending      map[ClientID][]decoded
	pendingCount int
	pendingDS    deleteSet
	missing      map[ClientID]uint64

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

	// MaxPendingStructs caps how many causally blocked structs may be
	// buffered. Zero means DefaultMaxPendingStructs.
	MaxPendingStructs int
}

// DefaultMaxPendingStructs is the default value of Options.MaxPendingStructs.
const DefaultMaxPendingStructs = 1 << 16

// NewDoc returns an empty Doc with a fresh random ClientID.
func NewDoc() *Doc { return NewDocWith(Options{}) }

// NewDocWith returns an empty Doc configured by opts.
func NewDocWith(opts Options) *Doc {
	c := opts.ClientID
	if c == 0 {
		c = newClientID()
	}
	maxPending := opts.MaxPendingStructs
	if maxPending == 0 {
		maxPending = DefaultMaxPendingStructs
	}
	return &Doc{
		clientID:   c,
		maxPending: maxPending,
		roots:      make(map[string]*Text),
		pending:    make(map[ClientID][]decoded),
		pendingDS:  deleteSet{},
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

// Resolve returns the Text p names and the rune index it points at now. A
// false ok means the anchored rune has not arrived here yet: hide that cursor.
func (d *Doc) Resolve(p Position) (t *Text, index int, ok bool) {
	if !p.Valid() {
		return nil, 0, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t = d.roots[p.name]
	if t == nil {
		return nil, 0, false // this replica has never seen that root
	}
	switch p.kind {
	case posStart:
		return t, 0, true
	case posEnd:
		return t, t.runeLen, true
	}
	if d.store.stateOf(p.item.Client) <= p.item.Clock {
		return t, 0, false // guessing here is how a cursor jumps to the wrong rune
	}
	it := d.store.get(p.item) // the run holding that clock, never a split
	if it.parent != t {
		return t, 0, false
	}
	r, _ := visibleIndexOf(it)
	return t, r + p.anchorBase(it), true
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
	ds := deleteSetFromStore(&d.store)
	// the buffered ranges are real deletions someone performed, and the peer
	// asking may well hold the structs they name
	ds.union(d.pendingDS)
	return encodeCanonical(structsSince(&d.store, since.m), ds)
}

// ApplyUpdate integrates u, which may repeat what this replica already holds.
// A struct whose anchors are missing is buffered and retried, never dropped.
// ErrPendingOverflow means the ready part of u was applied and the blocked
// remainder was discarded because the buffer is full.
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
	unapplied := applyDeleteSet(tx, ds)
	overflow := d.bufferStructs(leftover)
	if d.bufferDeleteSet(unapplied) {
		overflow = true
	}
	d.commit(tx)
	switch {
	case overflow:
		return ErrPendingOverflow
	case rejected:
		return badUpdate(0, "struct.originOrder")
	}
	return nil
}

// Pending reports the causal backlog: n buffered structs, and per client the
// lowest clock this replica has to reach before any of them can be integrated.
// An n that does not fall back to zero means sending a fresh sync on every link.
func (d *Doc) Pending() (n int, missing StateVector) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pendingCount, StateVector{m: maps.Clone(d.missing)}
}

// OnUpdate registers fn, called once per committed transaction that changed the
// document. The returned func unregisters fn and is idempotent.
func (d *Doc) OnUpdate(fn func(u Update, origin any)) (cancel func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return addObserver(d, &d.updObs, fn)
}
