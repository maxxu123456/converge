package converge

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// the worked first insert: client 42 writes "hi" into the Text "body"
var firstInsert = []byte{
	0xcf, 0x01, 0x01, // header
	0x01,                   // numClients
	0x2a, 0x01, 0x00, 0x01, // client 42, one run at clock 0, one struct
	0x04, 0x04, 'b', 'o', 'd', 'y', // info = hasParentName, "body"
	0x02, 'h', 'i', // content "hi"
	0x00, // delete set: no clients
}

func longNameUpdate(n int) []byte {
	b := []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x04}
	b = append(b, byte(n&0x7f)|0x80, byte(n>>7))
	b = append(b, strings.Repeat("n", n)...)
	b = append(b, 0x02, 'h', 'i', 0x00)
	return b
}

var acceptedUpdates = []struct {
	name string
	in   []byte
}{
	{"first insert", firstInsert},
	{"no structs and no deletes", []byte{0xcf, 0x01, 0x01, 0x00, 0x00}},
	{"deletes only", []byte{0xcf, 0x01, 0x01, 0x00, 0x01, 0x2a, 0x01, 0x00, 0x01}},
	{"two clients", []byte{
		0xcf, 0x01, 0x01,
		0x02,
		0x07, 0x01, 0x00, 0x01, 0x04, 0x04, 'b', 'o', 'd', 'y', 0x01, 'X',
		0x2a, 0x01, 0x00, 0x01, 0x04, 0x04, 'b', 'o', 'd', 'y', 0x02, 'h', 'i',
		0x00,
	}},
	{"two runs with a gap", []byte{
		0xcf, 0x01, 0x01,
		0x01,
		0x2a, 0x02,
		0x00, 0x01, 0x04, 0x04, 'b', 'o', 'd', 'y', 0x01, 'h',
		0x05, 0x01, 0x04, 0x04, 'b', 'o', 'd', 'y', 0x01, 'i',
		0x00,
	}},
	{"both origins", []byte{
		0xcf, 0x01, 0x01,
		0x01,
		0x2a, 0x01, 0x00, 0x01,
		0x03, 0x07, 0x02, 0x09, 0x00, // origin {7,2}, rightOrigin {9,0}
		0x02, 'h', 'i',
		0x00,
	}},
	{"multi byte clocks and ids", []byte{
		0xcf, 0x01, 0x01,
		0x01,
		0x81, 0x02, 0x01, 0x80, 0x04, 0x01, // client 257, one run at clock 512
		0x04, 0x01, 'b', 0x02, 'h', 'i',
		0x00,
	}},
	{"astral content", []byte{
		0xcf, 0x01, 0x01,
		0x01,
		0x2a, 0x01, 0x00, 0x01,
		0x04, 0x01, 'b',
		0x04, 0xf0, 0x9d, 0x84, 0x9e,
		0x00,
	}},
	{"many delete ranges", []byte{
		0xcf, 0x01, 0x01,
		0x00,
		0x02,
		0x07, 0x02, 0x00, 0x03, 0x02, 0x01, // client 7: [0,3) then [5,6)
		0x2a, 0x01, 0x04, 0x02, // client 42: [4,6)
	}},
	{"255 byte name", longNameUpdate(255)},
}

var rejectedUpdates = []struct {
	name string
	in   []byte
}{
	{"empty", nil},
	{"truncated header", []byte{0xcf, 0x01}},
	{"bad magic", []byte{0xce, 0x01, 0x01, 0x00, 0x00}},
	{"wrong kind", []byte{0xcf, 0x02, 0x01, 0x00, 0x00}},
	{"trailing bytes", append(append([]byte{}, firstInsert...), 0x00)},

	{"reserved info bit", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x00, 0x01,
		0x0c, 0x04, 'b', 'o', 'd', 'y', 0x02, 'h', 'i', 0x00}},
	{"all info bits", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x00, 0x01,
		0xff, 0x04, 'b', 'o', 'd', 'y', 0x02, 'h', 'i', 0x00}},
	{"no origins and no name", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x00, 0x01,
		0x00, 0x02, 'h', 'i', 0x00}},
	{"origin and a name", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x01, 0x01,
		0x05, 0x07, 0x00, 0x04, 'b', 'o', 'd', 'y', 0x02, 'h', 'i', 0x00}},

	{"empty content", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x00, 0x01,
		0x04, 0x04, 'b', 'o', 'd', 'y', 0x00, 0x00}},
	{"content is not utf8", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x00, 0x01,
		0x04, 0x04, 'b', 'o', 'd', 'y', 0x01, 0xff, 0x00}},

	{"empty name", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x00, 0x01,
		0x04, 0x00, 0x02, 'h', 'i', 0x00}},
	{"name is not utf8", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x00, 0x01,
		0x04, 0x01, 0xff, 0x02, 'h', 'i', 0x00}},
	{"256 byte name", longNameUpdate(256)},

	{"zero client in structs", []byte{0xcf, 0x01, 0x01, 0x01, 0x00, 0x01, 0x00, 0x01,
		0x04, 0x04, 'b', 'o', 'd', 'y', 0x02, 'h', 'i', 0x00}},
	{"zero client in an origin", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x00, 0x01,
		0x01, 0x00, 0x00, 0x02, 'h', 'i', 0x00}},
	{"zero client in deletes", []byte{0xcf, 0x01, 0x01, 0x00, 0x01, 0x00, 0x01, 0x00, 0x01}},

	{"descending clients", []byte{
		0xcf, 0x01, 0x01, 0x02,
		0x2a, 0x01, 0x00, 0x01, 0x04, 0x04, 'b', 'o', 'd', 'y', 0x02, 'h', 'i',
		0x07, 0x01, 0x00, 0x01, 0x04, 0x04, 'b', 'o', 'd', 'y', 0x01, 'X',
		0x00,
	}},
	{"repeated client", []byte{
		0xcf, 0x01, 0x01, 0x02,
		0x07, 0x01, 0x00, 0x01, 0x04, 0x04, 'b', 'o', 'd', 'y', 0x01, 'X',
		0x07, 0x01, 0x05, 0x01, 0x04, 0x04, 'b', 'o', 'd', 'y', 0x01, 'Y',
		0x00,
	}},
	{"descending delete clients", []byte{0xcf, 0x01, 0x01, 0x00, 0x02,
		0x2a, 0x01, 0x00, 0x01, 0x07, 0x01, 0x00, 0x01}},

	{"descending runs", []byte{
		0xcf, 0x01, 0x01, 0x01, 0x2a, 0x02,
		0x02, 0x01, 0x04, 0x01, 'b', 0x01, 'h',
		0x00, 0x01, 0x04, 0x01, 'b', 0x01, 'i',
		0x00,
	}},
	{"abutting runs", []byte{
		0xcf, 0x01, 0x01, 0x01, 0x2a, 0x02,
		0x00, 0x01, 0x04, 0x01, 'b', 0x01, 'h',
		0x01, 0x01, 0x04, 0x01, 'b', 0x01, 'i',
		0x00,
	}},
	{"overlapping runs", []byte{
		0xcf, 0x01, 0x01, 0x01, 0x2a, 0x02,
		0x00, 0x01, 0x04, 0x01, 'b', 0x02, 'h', 'i',
		0x01, 0x01, 0x04, 0x01, 'b', 0x01, 'i',
		0x00,
	}},
	// padded so the count survives the cheap size check and reaches the >= 1 rule
	{"no runs", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}},
	{"no structs in a run", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00}},

	{"origin names its own future", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x00, 0x01,
		0x01, 0x2a, 0x00, 0x02, 'h', 'i', 0x00}},
	{"right origin names its own future", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x00, 0x01,
		0x02, 0x2a, 0x05, 0x02, 'h', 'i', 0x00}},

	{"delete range of length zero", []byte{0xcf, 0x01, 0x01, 0x00, 0x01, 0x2a, 0x01, 0x00, 0x00}},
	{"abutting delete ranges", []byte{0xcf, 0x01, 0x01, 0x00, 0x01, 0x2a, 0x02, 0x00, 0x01, 0x00, 0x01}},
	{"no delete ranges", []byte{0xcf, 0x01, 0x01, 0x00, 0x01, 0x2a, 0x00, 0x00, 0x00}},

	{"run clock overflows", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01,
		0x01, 0x04, 0x01, 'b', 0x02, 'h', 'i', 0x00}},
	{"delete range overflows", []byte{0xcf, 0x01, 0x01, 0x00, 0x01, 0x2a, 0x01,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01, 0x02}},
	{"delete gap overflows", []byte{0xcf, 0x01, 0x01, 0x00, 0x01, 0x2a, 0x02,
		0x00, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01,
		0x01, 0x01}},

	{"non-minimal client count", []byte{0xcf, 0x01, 0x01, 0x81, 0x00, 0x2a, 0x01, 0x00, 0x01,
		0x04, 0x04, 'b', 'o', 'd', 'y', 0x02, 'h', 'i', 0x00}},
	{"non-minimal client id", []byte{0xcf, 0x01, 0x01, 0x01, 0xaa, 0x00, 0x01, 0x00, 0x01,
		0x04, 0x04, 'b', 'o', 'd', 'y', 0x02, 'h', 'i', 0x00}},
	{"non-minimal content length", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x00, 0x01,
		0x04, 0x04, 'b', 'o', 'd', 'y', 0x82, 0x00, 'h', 'i', 0x00}},

	{"client count overruns the input", []byte{0xcf, 0x01, 0x01, 0x7f, 0x2a, 0x01}},
	{"client count overflows", []byte{0xcf, 0x01, 0x01,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x02}},
	{"delete client count overruns the input", []byte{0xcf, 0x01, 0x01, 0x00, 0x7f, 0x2a, 0x01}},
}

func TestDecodeUpdateRejects(t *testing.T) {
	for _, c := range rejectedUpdates {
		_, _, err := decodeUpdate(c.in)
		if !errors.Is(err, ErrMalformedUpdate) {
			t.Errorf("%s: got %v, want ErrMalformedUpdate", c.name, err)
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

func TestDecodeUpdateUnsupportedVersion(t *testing.T) {
	b := append([]byte{}, firstInsert...)
	b[2] = 0x02
	_, _, err := decodeUpdate(b)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("got %v, want ErrUnsupportedVersion", err)
	}
	var de *DecodeError
	if !errors.As(err, &de) || de.Offset != 2 {
		t.Fatalf("want a *DecodeError at byte 2, got %v", err)
	}
}

func TestAcceptedUpdatesReencodeExactly(t *testing.T) {
	for _, c := range acceptedUpdates {
		structs, ds, err := decodeUpdate(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := encodeStructs(structs, ds); !bytes.Equal(got, c.in) {
			t.Errorf("%s: re-encoded to %x, want %x", c.name, got, c.in)
		}
	}
}

func TestDecodeUpdateTruncatedAtEveryOffset(t *testing.T) {
	for _, c := range acceptedUpdates {
		for i := 0; i < len(c.in); i++ {
			err := func() (err error) {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("%s: prefix of %d bytes panicked: %v", c.name, i, p)
					}
				}()
				_, _, err = decodeUpdate(c.in[:i])
				return
			}()
			if !errors.Is(err, ErrMalformedUpdate) {
				t.Fatalf("%s: prefix of %d bytes: got %v, want ErrMalformedUpdate", c.name, i, err)
			}
		}
	}
}

func TestDecodedContentDoesNotAliasTheInput(t *testing.T) {
	b := append([]byte{}, firstInsert...)
	structs, _, err := decodeUpdate(b)
	if err != nil {
		t.Fatal(err)
	}
	s := structs[42][0]
	for i := range b {
		b[i] = 0
	}
	if s.content != "hi" || s.parentName != "body" {
		t.Errorf("decoded strings alias the caller's buffer: %q %q", s.content, s.parentName)
	}
}

// A hostile count must be rejected against the bytes actually present, before
// it can size a map or a slice.
func TestHostileCountsAllocateAlmostNothing(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"clients", []byte{0xcf, 0x01, 0x01, 0xff, 0xff, 0xff, 0xff, 0x7f}},
		{"runs", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0xff, 0xff, 0xff, 0xff, 0x7f}},
		{"structs", []byte{0xcf, 0x01, 0x01, 0x01, 0x2a, 0x01, 0x00, 0xff, 0xff, 0xff, 0xff, 0x7f}},
		{"delete clients", []byte{0xcf, 0x01, 0x01, 0x00, 0xff, 0xff, 0xff, 0xff, 0x7f}},
		{"delete ranges", []byte{0xcf, 0x01, 0x01, 0x00, 0x01, 0x2a, 0xff, 0xff, 0xff, 0xff, 0x7f}},
	}
	for _, c := range cases {
		if _, _, err := decodeUpdate(c.in); !errors.Is(err, ErrMalformedUpdate) {
			t.Fatalf("%s: got %v, want ErrMalformedUpdate", c.name, err)
		}
		n := testing.AllocsPerRun(50, func() { decodeUpdate(c.in) })
		if n > 4 {
			t.Errorf("%s: %.0f allocations, want at most 4", c.name, n)
		}
	}
}

// str builds a struct for the validation tests, which need shapes the decoder
// would never hand over.
func str(c ClientID, clock uint64, origin, rightOrigin ID, runes uint32) decoded {
	s := decoded{
		client:      c,
		clock:       clock,
		origin:      origin,
		rightOrigin: rightOrigin,
		content:     strings.Repeat("x", int(runes)),
		runeLen:     runes,
		u16Len:      runes,
	}
	if origin.IsZero() && rightOrigin.IsZero() {
		s.parentName = "body"
	}
	return s
}

func TestValidateUpdateAcceptsTheDecodedTable(t *testing.T) {
	for _, c := range acceptedUpdates {
		structs, _, err := decodeUpdate(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if err := validateUpdate(structs); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestValidateUpdateAcceptsAChain(t *testing.T) {
	var none ID
	structs := map[ClientID][]decoded{
		1: {
			str(1, 0, none, none, 1),
			str(1, 1, ID{1, 0}, none, 1),
		},
		2: {str(2, 0, ID{1, 1}, none, 1)},
	}
	if err := validateUpdate(structs); err != nil {
		t.Fatalf("a causally ordered update was rejected: %v", err)
	}
}

func TestValidateUpdateRejects(t *testing.T) {
	var none ID
	cases := []struct {
		name  string
		field string
		in    map[ClientID][]decoded
	}{
		{"origin names its own future", "struct.origin", map[ClientID][]decoded{
			1: {str(1, 3, ID{1, 9}, none, 1)},
		}},
		{"origin names its own clock", "struct.origin", map[ClientID][]decoded{
			1: {str(1, 3, ID{1, 4}, none, 1)},
		}},
		{"right origin names its own future", "struct.rightOrigin", map[ClientID][]decoded{
			1: {str(1, 3, none, ID{1, 9}, 1)},
		}},
		{"overlapping same client ranges", "struct.clock", map[ClientID][]decoded{
			1: {str(1, 0, none, none, 4), str(1, 2, ID{2, 0}, none, 2)},
		}},
		{"two structs naming each other", "struct.originCycle", map[ClientID][]decoded{
			1: {str(1, 0, ID{2, 0}, none, 1)},
			2: {str(2, 0, ID{1, 0}, none, 1)},
		}},
		{"a cycle through a same client predecessor", "struct.originCycle", map[ClientID][]decoded{
			1: {str(1, 0, ID{2, 0}, none, 1), str(1, 1, none, none, 1)},
			2: {str(2, 0, ID{1, 1}, none, 1)},
		}},
		{"a cycle through right origins", "struct.originCycle", map[ClientID][]decoded{
			1: {str(1, 0, none, ID{2, 0}, 1)},
			2: {str(2, 0, none, ID{1, 0}, 1)},
		}},
	}
	for _, c := range cases {
		err := validateUpdate(c.in)
		if !errors.Is(err, ErrMalformedUpdate) {
			t.Errorf("%s: got %v, want ErrMalformedUpdate", c.name, err)
			continue
		}
		var de *DecodeError
		if !errors.As(err, &de) {
			t.Errorf("%s: error is not a *DecodeError: %v", c.name, err)
			continue
		}
		if de.Field != c.field {
			t.Errorf("%s: field %q, want %q", c.name, de.Field, c.field)
		}
	}
}

// The predecessor edge is what stops a general topological order from emitting
// two same-client structs out of clock order.
func TestValidateUpdateNeedsThePredecessorEdge(t *testing.T) {
	var none ID
	structs := map[ClientID][]decoded{
		1: {str(1, 0, ID{2, 0}, none, 1), str(1, 1, none, none, 1)},
		2: {str(2, 0, ID{1, 1}, none, 1)},
	}
	if err := validateUpdate(structs); err == nil {
		t.Fatal("the cycle only closes through client 1's predecessor edge")
	}
	// the same shape without the back reference is a legal partial order
	structs[2] = []decoded{str(2, 0, none, none, 1)}
	if err := validateUpdate(structs); err != nil {
		t.Fatalf("rejected an acyclic update: %v", err)
	}
}

// A struct's clock length is its content's rune count and never reaches the
// wire. Counting bytes instead puts every later struct at the wrong id.
func TestImplicitClocksCountRunes(t *testing.T) {
	b := []byte{
		0xcf, 0x01, 0x01,
		0x01,
		0x2a, 0x01, 0x00, 0x02, // client 42, one run at clock 0, two structs
		0x04, 0x01, 'b', 0x04, 0xf0, 0x9d, 0x84, 0x9e, // one four-byte rune
		0x01, 0x2a, 0x00, 0x01, 'x', // origin {42,0}, content "x"
		0x00,
	}
	structs, ds, err := decodeUpdate(b)
	if err != nil {
		t.Fatal(err)
	}
	got := structs[42]
	if len(got) != 2 {
		t.Fatalf("decoded %d structs, want 2", len(got))
	}
	if got[0].runeLen != 1 || got[0].u16Len != 2 {
		t.Errorf("astral struct: runeLen %d, u16Len %d, want 1 and 2", got[0].runeLen, got[0].u16Len)
	}
	if got[1].clock != 1 {
		t.Errorf("the second struct landed at clock %d, want 1", got[1].clock)
	}
	if !bytes.Equal(encodeStructs(structs, ds), b) {
		t.Error("re-encoding did not reproduce the input")
	}
}
