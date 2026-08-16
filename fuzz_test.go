package converge

import (
	"bytes"
	"testing"
)

// FuzzDecodeUpdate asserts decode is total: any bytes at all either fail, or
// re-encode to exactly the bytes they came from.
func FuzzDecodeUpdate(f *testing.F) {
	for _, c := range acceptedUpdates {
		f.Add(c.in)
	}
	for _, c := range rejectedUpdates {
		f.Add(c.in)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		structs, ds, err := decodeUpdate(b)
		if err != nil {
			return
		}
		if got := encodeStructs(structs, ds); !bytes.Equal(got, b) {
			t.Fatalf("decoded %x and re-encoded it to %x", b, got)
		}
	})
}
