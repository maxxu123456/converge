package converge

import (
	"bytes"
	"encoding"
	"errors"
	"slices"
	"testing"

	"github.com/maxxu123456/converge/internal/wire"
)

var (
	_ encoding.BinaryMarshaler   = StateVector{}
	_ encoding.BinaryUnmarshaler = (*StateVector)(nil)
)

func mustMarshal(t *testing.T, sv StateVector) []byte {
	t.Helper()
	b, err := sv.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	return b
}

// wireClients reads back the client ids a blob names, in encoded order.
func wireClients(t *testing.T, b []byte) []ClientID {
	t.Helper()
	r := &wire.Reader{B: b, I: 3}
	n, err := r.Uvarint()
	if err != nil {
		t.Fatalf("numClients: %v", err)
	}
	cs := make([]ClientID, 0, n)
	for i := uint64(0); i < n; i++ {
		c, err := r.Uvarint()
		if err != nil {
			t.Fatalf("clientID %d: %v", i, err)
		}
		if _, err := r.Uvarint(); err != nil {
			t.Fatalf("clock %d: %v", i, err)
		}
		cs = append(cs, ClientID(c))
	}
	return cs
}

func TestStateVectorZeroValue(t *testing.T) {
	var sv StateVector
	if got := sv.Get(42); got != 0 {
		t.Errorf("Get on the zero value: got %d, want 0", got)
	}
	if got := sv.Clients(); len(got) != 0 {
		t.Errorf("Clients on the zero value: got %v", got)
	}
	if got := sv.String(); got != "{}" {
		t.Errorf("String on the zero value: got %q, want %q", got, "{}")
	}

	b := mustMarshal(t, sv)
	want := []byte{0xcf, 0x02, 0x01, 0x00}
	if !bytes.Equal(b, want) {
		t.Fatalf("got % x, want % x", b, want)
	}

	back, err := ParseStateVector(want)
	if err != nil {
		t.Fatalf("ParseStateVector: %v", err)
	}
	if got := back.Get(42); got != 0 {
		t.Errorf("parsed empty vector: Get returned %d", got)
	}
	if got := mustMarshal(t, back); !bytes.Equal(got, want) {
		t.Errorf("re-marshal: got % x, want % x", got, want)
	}
}

func TestStateVectorRoundTrip(t *testing.T) {
	sv := StateVector{m: map[ClientID]uint64{
		300:        7,
		2:          1,
		0xdeadbeef: 1 << 40,
		1:          ^uint64(0),
	}}
	b := mustMarshal(t, sv)

	got, err := ParseStateVector(b)
	if err != nil {
		t.Fatalf("ParseStateVector: %v", err)
	}
	for c, clock := range sv.m {
		if got.Get(c) != clock {
			t.Errorf("client %s: got %d, want %d", c, got.Get(c), clock)
		}
	}
	if got.Get(99) != 0 {
		t.Errorf("unknown client: got %d, want 0", got.Get(99))
	}
	if again := mustMarshal(t, got); !bytes.Equal(again, b) {
		t.Errorf("round trip changed the bytes:\n got % x\nwant % x", again, b)
	}

	var un StateVector
	if err := un.UnmarshalBinary(b); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}
	if un.String() != got.String() {
		t.Errorf("UnmarshalBinary: got %s, want %s", un.String(), got.String())
	}
}

func TestStateVectorClientsAscend(t *testing.T) {
	sv := StateVector{m: map[ClientID]uint64{
		9: 1, 1: 2, 1 << 63: 3, 5: 4, 128: 5, 0xffff: 6, 2: 7, 300: 8,
	}}
	want := []ClientID{1, 2, 5, 9, 128, 300, 0xffff, 1 << 63}

	if got := sv.Clients(); !slices.Equal(got, want) {
		t.Errorf("Clients: got %v, want %v", got, want)
	}
	if got := wireClients(t, mustMarshal(t, sv)); !slices.Equal(got, want) {
		t.Errorf("encoded order: got %v, want %v", got, want)
	}
}

func TestStateVectorMarshalIsStable(t *testing.T) {
	sv := StateVector{m: map[ClientID]uint64{
		9: 1, 1: 2, 1 << 63: 3, 5: 4, 128: 5, 0xffff: 6, 2: 7, 300: 8,
	}}
	first := mustMarshal(t, sv)
	for i := 0; i < 100; i++ {
		if got := mustMarshal(t, sv); !bytes.Equal(got, first) {
			t.Fatalf("marshal %d differs:\n got % x\nwant % x", i, got, first)
		}
	}
}

func TestStateVectorOmitsZeroEntries(t *testing.T) {
	with := StateVector{m: map[ClientID]uint64{7: 3, 8: 0, 9: 5}}
	without := StateVector{m: map[ClientID]uint64{7: 3, 9: 5}}

	if got := with.Clients(); !slices.Equal(got, []ClientID{7, 9}) {
		t.Errorf("Clients: got %v, want [7 9]", got)
	}
	if got, want := mustMarshal(t, with), mustMarshal(t, without); !bytes.Equal(got, want) {
		t.Errorf("zero entry reached the wire:\n got % x\nwant % x", got, want)
	}
	if got, want := with.String(), "{7:3, 9:5}"; got != want {
		t.Errorf("String: got %q, want %q", got, want)
	}
}

func TestParseStateVectorMalformed(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"header truncated", []byte{0xcf, 0x02}},
		{"bad magic", []byte{0x00, 0x02, 0x01, 0x00}},
		{"wrong kind", []byte{0xcf, 0x01, 0x01, 0x00}},
		{"count truncated", []byte{0xcf, 0x02, 0x01}},
		{"client truncated", []byte{0xcf, 0x02, 0x01, 0x01}},
		{"clock truncated", []byte{0xcf, 0x02, 0x01, 0x01, 0x2a}},
		{"zero client", []byte{0xcf, 0x02, 0x01, 0x01, 0x00, 0x01}},
		{"zero clock", []byte{0xcf, 0x02, 0x01, 0x01, 0x2a, 0x00}},
		{"repeated client", []byte{0xcf, 0x02, 0x01, 0x02, 0x05, 0x01, 0x05, 0x01}},
		{"descending clients", []byte{0xcf, 0x02, 0x01, 0x02, 0x05, 0x01, 0x03, 0x01}},
		{"trailing bytes", []byte{0xcf, 0x02, 0x01, 0x01, 0x2a, 0x01, 0x00}},
		{"non-minimal client", []byte{0xcf, 0x02, 0x01, 0x01, 0xaa, 0x00, 0x01}},
		{"non-minimal clock", []byte{0xcf, 0x02, 0x01, 0x01, 0x2a, 0x81, 0x00}},
		{"count overruns input", []byte{0xcf, 0x02, 0x01, 0x7f, 0x2a, 0x01}},
		{"count overflows", []byte{0xcf, 0x02, 0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}},
	}
	for _, c := range cases {
		_, err := ParseStateVector(c.in)
		if !errors.Is(err, ErrMalformedStateVector) {
			t.Errorf("%s: got %v, want ErrMalformedStateVector", c.name, err)
			continue
		}
		var de *DecodeError
		if !errors.As(err, &de) {
			t.Errorf("%s: error is not a *DecodeError: %v", c.name, err)
			continue
		}
		if de.Field == "" {
			t.Errorf("%s: *DecodeError carries no field name", c.name)
		}
	}
}

func TestParseStateVectorUnsupportedVersion(t *testing.T) {
	_, err := ParseStateVector([]byte{0xcf, 0x02, 0x02, 0x00})
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("got %v, want ErrUnsupportedVersion", err)
	}
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("error is not a *DecodeError: %v", err)
	}
	if de.Offset != 2 {
		t.Errorf("offset: got %d, want 2", de.Offset)
	}
}

func TestParseStateVectorTruncatedAtEveryOffset(t *testing.T) {
	full := mustMarshal(t, StateVector{m: map[ClientID]uint64{1: 1, 300: 1 << 40, 0xdeadbeef: 2}})
	for i := 0; i < len(full); i++ {
		err := func() (err error) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("prefix of %d bytes panicked: %v", i, p)
				}
			}()
			_, err = ParseStateVector(full[:i])
			return
		}()
		if !errors.Is(err, ErrMalformedStateVector) {
			t.Fatalf("prefix of %d bytes: got %v, want ErrMalformedStateVector", i, err)
		}
	}
}
