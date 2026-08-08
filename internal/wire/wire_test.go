package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"testing"
)

// sample writes one of every primitive, so the truncation table covers a
// varint boundary, a length prefix and a payload.
func sample() []byte {
	w := &Writer{}
	w.Byte(0xCF)
	w.Uvarint(0)
	w.Uvarint(300)
	w.Uvarint(math.MaxUint64)
	w.Bytes([]byte{1, 2, 3})
	w.String("hello")
	w.Bytes(nil)
	w.String("")
	return w.B
}

func readSample(r *Reader) error {
	b, err := r.Byte()
	if err != nil {
		return err
	}
	if b != 0xCF {
		return fmt.Errorf("byte: got %#x, want 0xcf", b)
	}
	for _, want := range []uint64{0, 300, math.MaxUint64} {
		v, err := r.Uvarint()
		if err != nil {
			return err
		}
		if v != want {
			return fmt.Errorf("uvarint: got %d, want %d", v, want)
		}
	}
	raw, err := r.Bytes()
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, []byte{1, 2, 3}) {
		return fmt.Errorf("bytes: got %v, want [1 2 3]", raw)
	}
	s, err := r.String()
	if err != nil {
		return err
	}
	if s != "hello" {
		return fmt.Errorf("string: got %q, want %q", s, "hello")
	}
	if raw, err = r.Bytes(); err != nil {
		return err
	} else if len(raw) != 0 {
		return fmt.Errorf("empty bytes: got %v", raw)
	}
	if s, err = r.String(); err != nil {
		return err
	} else if s != "" {
		return fmt.Errorf("empty string: got %q", s)
	}
	return nil
}

func readSampleFrom(b []byte) (panicked any, err error) {
	defer func() { panicked = recover() }()
	err = readSample(&Reader{B: b})
	return
}

func TestRoundTrip(t *testing.T) {
	full := sample()
	r := &Reader{B: full}
	if err := readSample(r); err != nil {
		t.Fatal(err)
	}
	if r.Remaining() != 0 {
		t.Fatalf("%d bytes left unread", r.Remaining())
	}
}

func TestTruncateAtEveryOffset(t *testing.T) {
	full := sample()
	for i := 0; i < len(full); i++ {
		panicked, err := readSampleFrom(full[:i])
		if panicked != nil {
			t.Fatalf("prefix of %d bytes panicked: %v", i, panicked)
		}
		if !errors.Is(err, ErrTruncated) {
			t.Fatalf("prefix of %d bytes: got %v, want ErrTruncated", i, err)
		}
	}
}

func TestUvarintRoundTrip(t *testing.T) {
	values := []uint64{
		0, 1, 2, 126, 127, 128, 129, 255, 256, 16383, 16384,
		1 << 20, 1<<32 - 1, 1 << 32, 1 << 56, 1 << 63, math.MaxUint64 - 1, math.MaxUint64,
	}
	for _, v := range values {
		w := &Writer{}
		w.Uvarint(v)
		if !bytes.Equal(w.B, binary.AppendUvarint(nil, v)) {
			t.Errorf("%d: writer disagrees with binary.AppendUvarint", v)
		}
		r := &Reader{B: w.B}
		got, err := r.Uvarint()
		if err != nil {
			t.Errorf("%d: %v", v, err)
			continue
		}
		if got != v {
			t.Errorf("got %d, want %d", got, v)
		}
		if r.Remaining() != 0 {
			t.Errorf("%d: %d bytes left unread", v, r.Remaining())
		}
	}
}

func TestUvarintRejects(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want error
	}{
		{"padded zero", []byte{0x80, 0x00}, ErrNonMinimal},
		{"padded one", []byte{0x81, 0x00}, ErrNonMinimal},
		{"padded three bytes", []byte{0xff, 0xff, 0x00}, ErrNonMinimal},
		{"padded to ten bytes", []byte{0x81, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x00}, ErrNonMinimal},
		{"tenth byte above one", []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x02}, ErrOverflow},
		{"eleven bytes", []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01}, ErrOverflow},
		{"continuation then nothing", []byte{0x80}, ErrTruncated},
		{"empty", nil, ErrTruncated},
	}
	for _, c := range cases {
		r := &Reader{B: c.in}
		_, err := r.Uvarint()
		if !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

func TestMaxUvarintIsAccepted(t *testing.T) {
	r := &Reader{B: []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}}
	v, err := r.Uvarint()
	if err != nil {
		t.Fatal(err)
	}
	if v != math.MaxUint64 {
		t.Fatalf("got %d, want %d", v, uint64(math.MaxUint64))
	}
}

func TestHugeLengthIsTruncatedNotPanic(t *testing.T) {
	// int(n) of a length this size wraps negative on 64-bit
	b := append(binary.AppendUvarint(nil, math.MaxUint64), 'a')
	panicked, err := func() (panicked any, err error) {
		defer func() { panicked = recover() }()
		_, err = (&Reader{B: b}).Bytes()
		return
	}()
	if panicked != nil {
		t.Fatalf("panicked: %v", panicked)
	}
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("got %v, want ErrTruncated", err)
	}
}

func TestBytesAliasesAndStringCopies(t *testing.T) {
	w := &Writer{}
	w.Bytes([]byte("abc"))
	w.String("xyz")
	buf := w.B

	r := &Reader{B: buf}
	raw, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.String()
	if err != nil {
		t.Fatal(err)
	}
	for i := range buf {
		buf[i] ^= 0xff
	}
	if string(raw) == "abc" {
		t.Error("Bytes copied, but callers are told it aliases and must clone")
	}
	if s != "xyz" {
		t.Errorf("String aliased the input buffer: got %q", s)
	}
}
