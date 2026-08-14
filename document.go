package converge

import "sync"

// Doc is a CRDT document: named Text values sharing one replica identity and
// one causal history. Safe for concurrent use, but never copy one.
type Doc struct {
	mu sync.Mutex // guards everything below, never held across io or user code

	clientID ClientID
	roots    map[string]*Text
	store    structStore
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
