package converge

import (
	"slices"
	"strconv"
	"strings"

	"github.com/maxxu123456/converge/internal/wire"
)

// StateVector records, per client, the next clock a replica expects. The zero
// value means "I have nothing" and is a valid argument everywhere.
type StateVector struct {
	m map[ClientID]uint64
}

// Get returns the next clock expected from c, or 0 if c is unknown.
func (sv StateVector) Get(c ClientID) uint64 { return sv.m[c] }

// Clients returns every client named by sv, ascending.
func (sv StateVector) Clients() []ClientID {
	cs := make([]ClientID, 0, len(sv.m))
	for c, clock := range sv.m {
		// clock 0 is indistinguishable from absent, so it names nobody
		if clock != 0 {
			cs = append(cs, c)
		}
	}
	slices.Sort(cs)
	return cs
}

// String renders sv as {client:clock, ...}, ascending, for diagnostics.
func (sv StateVector) String() string {
	var b strings.Builder
	b.WriteByte('{')
	for i, c := range sv.Clients() {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(c.String())
		b.WriteByte(':')
		b.WriteString(strconv.FormatUint(sv.m[c], 10))
	}
	b.WriteByte('}')
	return b.String()
}

// MarshalBinary encodes sv canonically. It never returns an error.
func (sv StateVector) MarshalBinary() ([]byte, error) {
	cs := sv.Clients()
	w := &wire.Writer{B: make([]byte, 0, 4+4*len(cs))}
	writeHeader(w, kindStateVector)
	w.Uvarint(uint64(len(cs)))
	for _, c := range cs {
		w.Uvarint(uint64(c))
		w.Uvarint(sv.m[c])
	}
	return w.B, nil
}

// UnmarshalBinary decodes bytes produced by MarshalBinary. sv is left
// untouched unless the whole blob parses.
func (sv *StateVector) UnmarshalBinary(b []byte) error {
	parsed, err := ParseStateVector(b)
	if err != nil {
		return err
	}
	*sv = parsed
	return nil
}

// ParseStateVector decodes a state vector blob.
func ParseStateVector(b []byte) (StateVector, error) {
	r := &wire.Reader{B: b}
	if err := readHeader(r, kindStateVector, ErrMalformedStateVector); err != nil {
		return StateVector{}, err
	}
	n, err := r.Uvarint()
	if err != nil {
		return StateVector{}, decodeErr(r.I, "stateVector.numClients", ErrMalformedStateVector)
	}
	// every entry costs at least two bytes, so a bogus count cannot size a map
	if n > uint64(r.Remaining()/2) {
		return StateVector{}, decodeErr(r.I, "stateVector.numClients", ErrMalformedStateVector)
	}
	m := make(map[ClientID]uint64, n)
	var prev ClientID
	for i := uint64(0); i < n; i++ {
		off := r.I
		raw, err := r.Uvarint()
		if err != nil {
			return StateVector{}, decodeErr(r.I, "stateVector.clientID", ErrMalformedStateVector)
		}
		c := ClientID(raw)
		if c == 0 || (i > 0 && c <= prev) {
			return StateVector{}, decodeErr(off, "stateVector.clientID", ErrMalformedStateVector)
		}
		off = r.I
		clock, err := r.Uvarint()
		if err != nil {
			return StateVector{}, decodeErr(r.I, "stateVector.clock", ErrMalformedStateVector)
		}
		// a zero clock is omitted, so seeing one means two encodings of one state
		if clock == 0 {
			return StateVector{}, decodeErr(off, "stateVector.clock", ErrMalformedStateVector)
		}
		m[c] = clock
		prev = c
	}
	if r.Remaining() != 0 {
		return StateVector{}, decodeErr(r.I, "stateVector.trailing", ErrMalformedStateVector)
	}
	return StateVector{m: m}, nil
}
