package converge

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var regolden = flag.Bool("update", false, "rewrite the golden files under testdata/golden")

const hexDigits = "0123456789abcdef"

// hexDump renders b the way the golden files store it, sixteen bytes a line.
func hexDump(b []byte) string {
	var sb strings.Builder
	for i, c := range b {
		if i > 0 {
			if i%16 == 0 {
				sb.WriteByte('\n')
			} else {
				sb.WriteByte(' ')
			}
		}
		sb.WriteByte(hexDigits[c>>4])
		sb.WriteByte(hexDigits[c&0x0f])
	}
	sb.WriteByte('\n')
	return sb.String()
}

func checkGolden(t *testing.T, name string, b []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".hex")
	got := hexDump(b)
	if *regolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (regenerate with go test -update)", name, err)
	}
	if got != string(want) {
		t.Errorf("%s moved on the wire:\ngot\n%swant\n%s", name, got, want)
	}
}

// splitRun shatters s into single-rune fragments the way repeated splits would,
// origins and all.
func splitRun(s decoded) []decoded {
	out := make([]decoded, 0, s.runeLen)
	clock := s.clock
	for i, r := range []rune(s.content) {
		f := decoded{
			client:      s.client,
			clock:       clock,
			origin:      ID{s.client, clock - 1},
			rightOrigin: s.rightOrigin,
			content:     string(r),
			runeLen:     1,
			u16Len:      utf16LenOf(string(r)),
		}
		if i == 0 {
			f.origin = s.origin
			f.parentName = s.parentName
		}
		out = append(out, f)
		clock++
	}
	return out
}

func TestFirstInsertEncodesToTheDocumentedBytes(t *testing.T) {
	got := encodeCanonical(map[ClientID][]decoded{
		42: {{client: 42, parentName: "body", content: "hi", runeLen: 2, u16Len: 2}},
	}, deleteSet{})
	if !bytes.Equal(got, firstInsert) {
		t.Fatalf("got  %x\nwant %x", got, firstInsert)
	}
	if len(got) != 18 {
		t.Errorf("%d bytes, want 18", len(got))
	}
	checkGolden(t, "first_insert", got)
}

func TestTwoConcurrentInsertsEncodeInClientOrder(t *testing.T) {
	got := encodeCanonical(map[ClientID][]decoded{
		42: {{client: 42, parentName: "body", content: "hi", runeLen: 2, u16Len: 2}},
		7:  {{client: 7, parentName: "body", content: "X", runeLen: 1, u16Len: 1}},
	}, deleteSet{})
	if len(got) != 30 {
		t.Errorf("%d bytes, want 30", len(got))
	}
	// 7 sorts before 42, so "X" precedes "hi" on every replica
	if got[4] != 0x07 || got[16] != 0x2a {
		t.Errorf("clients came out as %#x and %#x", got[4], got[16])
	}
	checkGolden(t, "two_clients", got)
}

func TestCanonicalEncodingIsMapOrderIndependent(t *testing.T) {
	structs := make(map[ClientID][]decoded, 24)
	ds := deleteSet{}
	for c := ClientID(1); c <= 24; c++ {
		structs[c] = []decoded{{client: c, parentName: "body", content: "ab", runeLen: 2, u16Len: 2}}
		ds[c] = []idRange{{clock: 0, length: 1}}
	}
	first := encodeCanonical(structs, ds)
	for i := 0; i < 1000; i++ {
		if got := encodeCanonical(structs, ds); !bytes.Equal(got, first) {
			t.Fatalf("encode %d differs:\ngot  %x\nwant %x", i, got, first)
		}
	}
}

func TestFoldErasesSplitHistory(t *testing.T) {
	for _, s := range []decoded{
		{client: 42, parentName: "body", content: "hello", runeLen: 5, u16Len: 5},
		{client: 42, clock: 9, origin: ID{7, 3}, rightOrigin: ID{7, 4}, content: "hello", runeLen: 5, u16Len: 5},
	} {
		whole := encodeCanonical(map[ClientID][]decoded{42: {s}}, deleteSet{})
		split := map[ClientID][]decoded{42: splitRun(s)}
		if got := encodeCanonical(split, deleteSet{}); !bytes.Equal(got, whole) {
			t.Errorf("folded to %x, want %x", got, whole)
		}
		if got := encodeStructs(split, deleteSet{}); bytes.Equal(got, whole) {
			t.Error("encodeStructs folded, it must emit exactly what it was given")
		}
	}
}

func TestFoldStopsWhereASplitCouldNotHaveCut(t *testing.T) {
	base := decoded{client: 42, parentName: "body", content: "ab", runeLen: 2, u16Len: 2}
	cases := []struct {
		name string
		next decoded
	}{
		{"a clock gap", decoded{client: 42, clock: 3, origin: ID{42, 2}, content: "c", runeLen: 1, u16Len: 1}},
		{"another right origin", decoded{client: 42, clock: 2, origin: ID{42, 1}, rightOrigin: ID{9, 0}, content: "c", runeLen: 1, u16Len: 1}},
		{"an origin that is not the predecessor", decoded{client: 42, clock: 2, origin: ID{9, 0}, content: "c", runeLen: 1, u16Len: 1}},
		{"a parent name of its own", decoded{client: 42, clock: 2, origin: ID{42, 1}, parentName: "other", content: "c", runeLen: 1, u16Len: 1}},
	}
	for _, c := range cases {
		if got := foldRuns([]decoded{base, c.next}); len(got) != 2 {
			t.Errorf("%s: folded into %d structs, want 2", c.name, len(got))
		}
	}
}

func TestFoldedRunSplitsBackIntoTheSameFields(t *testing.T) {
	s := decoded{client: 42, clock: 4, origin: ID{7, 1}, rightOrigin: ID{7, 2}, content: "hello", runeLen: 5, u16Len: 5}
	u := encodeCanonical(map[ClientID][]decoded{42: splitRun(s)}, deleteSet{})
	structs, ds, err := decodeUpdate(u)
	if err != nil {
		t.Fatal(err)
	}
	got := structs[42]
	if len(got) != 1 {
		t.Fatalf("decoded %d structs, want 1", len(got))
	}
	if got[0] != s {
		t.Errorf("round trip gave %+v, want %+v", got[0], s)
	}
	if again := encodeCanonical(structs, ds); !bytes.Equal(again, u) {
		t.Errorf("re-encoded to %x, want %x", again, u)
	}
}

// The split a delete leaves behind is in-memory only. What goes on the wire is
// the run as it was written, plus a delete range.
func TestASplitRunStillEncodesAsOneStruct(t *testing.T) {
	whole := decoded{client: 42, parentName: "body", content: "hi", runeLen: 2, u16Len: 2}
	got := encodeCanonical(
		map[ClientID][]decoded{42: splitRun(whole)},
		deleteSet{42: {{clock: 0, length: 1}}},
	)
	want := append([]byte{}, firstInsert[:len(firstInsert)-1]...)
	want = append(want, 0x01, 0x2a, 0x01, 0x00, 0x01)
	if !bytes.Equal(got, want) {
		t.Fatalf("got  %x\nwant %x", got, want)
	}
}

func TestDeleteSetGapsRunFromTheLastEnd(t *testing.T) {
	got := encodeCanonical(
		map[ClientID][]decoded{},
		deleteSet{42: {{clock: 3, length: 2}, {clock: 9, length: 1}}},
	)
	want := []byte{0xcf, 0x01, 0x01, 0x00, 0x01, 0x2a, 0x02, 0x03, 0x02, 0x04, 0x01}
	if !bytes.Equal(got, want) {
		t.Fatalf("got  %x\nwant %x", got, want)
	}
}

func TestEmptyUpdateIsFiveBytes(t *testing.T) {
	got := encodeCanonical(map[ClientID][]decoded{}, deleteSet{})
	if want := []byte{0xcf, 0x01, 0x01, 0x00, 0x00}; !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}
