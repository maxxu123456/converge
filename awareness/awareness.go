package awareness

import (
	"bytes"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/maxxu123456/converge"
	"github.com/maxxu123456/converge/internal/wire"
)

// ErrMalformedAwarenessUpdate is returned when bytes offered to Apply violate
// the awareness wire format.
var ErrMalformedAwarenessUpdate = errors.New("awareness: malformed awareness update")

// the magic, kind and version every converge blob starts with
var header = [3]byte{0xcf, 0x03, 0x01}

// entry is one client's presence. A nil state means removed, and the clock is
// still remembered, so a message in flight cannot resurrect a departed peer.
type entry struct {
	clock    uint64
	state    []byte
	lastSeen time.Time
}

// Awareness tracks per-client presence blobs: cursors, names, colours. State
// payloads are opaque, since converge does not own your schema. Safe for
// concurrent use.
type Awareness struct {
	mu      sync.Mutex
	local   converge.ClientID
	entries map[converge.ClientID]entry
}

// New returns an Awareness whose local identity is d's ClientID. Taking the Doc
// rather than a ClientID makes an identity mismatch unconstructible.
func New(d *converge.Doc) *Awareness {
	return &Awareness{local: d.ClientID(), entries: make(map[converge.ClientID]entry)}
}

// ClientID returns the local identity.
func (a *Awareness) ClientID() converge.ClientID { return a.local }

// SetLocalState publishes local presence, bumping the local clock, and returns
// the update to broadcast. A nil state means "I am gone". state is copied.
func (a *Awareness) SetLocalState(state []byte) (update []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries[a.local] = entry{
		clock:    a.entries[a.local].clock + 1,
		state:    bytes.Clone(state),
		lastSeen: time.Now(),
	}
	return a.encode([]converge.ClientID{a.local})
}

// LocalState returns a copy of the local state, or nil.
func (a *Awareness) LocalState() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return bytes.Clone(a.entries[a.local].state)
}

// States returns a copy of every live state, including the local one.
func (a *Awareness) States() map[converge.ClientID][]byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := make(map[converge.ClientID][]byte, len(a.entries))
	for c, e := range a.entries {
		if e.state != nil {
			m[c] = bytes.Clone(e.state)
		}
	}
	return m
}

// Encode serializes the states of the given clients. No arguments means every
// known client, which is what a newly connected peer needs.
func (a *Awareness) Encode(clients ...converge.ClientID) []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(clients) == 0 {
		return a.encode(a.known())
	}
	cs := slices.Clone(clients)
	slices.Sort(cs)
	return a.encode(slices.Compact(cs))
}

// Apply integrates a peer's awareness update.
//
// changed is exactly the set of clients whose state changed, the repaint list.
// Nothing is applied when err is non-nil.
func (a *Awareness) Apply(update []byte) (changed []converge.ClientID, reply []byte, err error) {
	entries, err := decode(update)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range entries {
		known, seen := a.entries[e.client]
		// strictly greater: Remove and SetLocalState(nil) both write known+1,
		// so an equal clock is always something already heard
		if seen && e.clock <= known.clock {
			continue
		}
		a.entries[e.client] = entry{clock: e.clock, state: e.state, lastSeen: now}
		changed = append(changed, e.client)
	}
	return changed, nil, nil
}

// Remove announces that the given clients are gone and returns the update to
// broadcast. A server calls it with a peer's id when that socket closes.
func (a *Awareness) Remove(clients ...converge.ClientID) (update []byte) {
	cs := slices.Clone(clients)
	slices.Sort(cs)
	cs = slices.Compact(cs)
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range cs {
		a.entries[c] = entry{clock: a.entries[c].clock + 1, lastSeen: now}
	}
	return a.encode(cs)
}

// known returns every client this replica has an entry for, ascending.
func (a *Awareness) known() []converge.ClientID {
	cs := make([]converge.ClientID, 0, len(a.entries))
	for c := range a.entries {
		cs = append(cs, c)
	}
	slices.Sort(cs)
	return cs
}

// encode serializes clients, which must be ascending and deduplicated, skipping
// any this replica has never heard of. The caller holds the lock.
func (a *Awareness) encode(clients []converge.ClientID) []byte {
	have := make([]converge.ClientID, 0, len(clients))
	for _, c := range clients {
		if _, ok := a.entries[c]; ok {
			have = append(have, c)
		}
	}
	w := &wire.Writer{B: make([]byte, 0, 8+24*len(have))}
	w.B = append(w.B, header[:]...)
	w.Uvarint(uint64(len(have)))
	for _, c := range have {
		e := a.entries[c]
		w.Uvarint(uint64(c))
		w.Uvarint(e.clock)
		if e.state == nil {
			w.Uvarint(0) // removed, as opposed to present and empty
			continue
		}
		w.Uvarint(uint64(len(e.state)) + 1)
		w.B = append(w.B, e.state...)
	}
	return w.B
}

// wireEntry is one decoded client record. A nil state means removed.
type wireEntry struct {
	client converge.ClientID
	clock  uint64
	state  []byte
}

func decode(b []byte) ([]wireEntry, error) {
	r := &wire.Reader{B: b}
	if len(b) < 3 || b[0] != header[0] || b[1] != header[1] || b[2] != header[2] {
		return nil, ErrMalformedAwarenessUpdate
	}
	r.I = 3
	n, err := r.Uvarint()
	if err != nil {
		return nil, ErrMalformedAwarenessUpdate
	}
	// every record costs at least three bytes, so a bogus count sizes nothing
	if n > uint64(r.Remaining()/3) {
		return nil, ErrMalformedAwarenessUpdate
	}
	out := make([]wireEntry, 0, n)
	var prev converge.ClientID
	for i := uint64(0); i < n; i++ {
		raw, err := r.Uvarint()
		if err != nil {
			return nil, ErrMalformedAwarenessUpdate
		}
		c := converge.ClientID(raw)
		if c == 0 || (i > 0 && c <= prev) {
			return nil, ErrMalformedAwarenessUpdate
		}
		prev = c
		clock, err := r.Uvarint()
		if err != nil {
			return nil, ErrMalformedAwarenessUpdate
		}
		lenPlus1, err := r.Uvarint()
		if err != nil {
			return nil, ErrMalformedAwarenessUpdate
		}
		var state []byte
		if lenPlus1 > 0 {
			if lenPlus1-1 > uint64(r.Remaining()) {
				return nil, ErrMalformedAwarenessUpdate
			}
			// non-nil even when empty: present and empty is not removed
			state = make([]byte, lenPlus1-1)
			copy(state, r.B[r.I:])
			r.I += len(state)
		}
		out = append(out, wireEntry{client: c, clock: clock, state: state})
	}
	if r.Remaining() != 0 {
		return nil, ErrMalformedAwarenessUpdate
	}
	return out, nil
}
