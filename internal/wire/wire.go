// Package wire is the byte level of converge's blob formats: minimal-form
// LEB128 varints and length-prefixed bytes over a plain []byte.
package wire

import (
	"encoding/binary"
	"errors"
)

// Writer appends to B. Appending cannot fail, so no method returns an error.
type Writer struct{ B []byte }

// Byte appends one raw byte.
func (w *Writer) Byte(b byte) { w.B = append(w.B, b) }

// Uvarint appends v in minimal-form LEB128.
func (w *Writer) Uvarint(v uint64) { w.B = binary.AppendUvarint(w.B, v) }

// Bytes appends the length as a uvarint, then the raw bytes.
func (w *Writer) Bytes(b []byte) {
	w.Uvarint(uint64(len(b)))
	w.B = append(w.B, b...)
}

// String appends the length as a uvarint, then the UTF-8 bytes, with no
// []byte conversion.
func (w *Writer) String(s string) {
	w.Uvarint(uint64(len(s)))
	w.B = append(w.B, s...)
}

var (
	ErrTruncated  = errors.New("wire: truncated input")
	ErrNonMinimal = errors.New("wire: non-minimal varint")
)

// Reader decodes from B, starting at I. A failed read leaves I where the
// reader stopped, since any error aborts the whole blob.
type Reader struct {
	B []byte
	I int
}

// Remaining returns how many bytes are left unread.
func (r *Reader) Remaining() int { return len(r.B) - r.I }

// Byte reads one raw byte.
func (r *Reader) Byte() (byte, error) {
	if r.I >= len(r.B) {
		return 0, ErrTruncated
	}
	b := r.B[r.I]
	r.I++
	return b, nil
}

// Uvarint decodes a minimal-form LEB128 value. An encoding longer than
// binary.AppendUvarint would produce for the same number is rejected.
func (r *Reader) Uvarint() (uint64, error) {
	var v uint64
	for n := uint(0); ; n++ {
		b, err := r.Byte()
		if err != nil {
			return 0, err
		}
		if b < 0x80 {
			// a trailing zero group means the writer padded the value out
			if n > 0 && b == 0 {
				return 0, ErrNonMinimal
			}
			return v | uint64(b)<<(7*n), nil
		}
		v |= uint64(b&0x7f) << (7 * n)
	}
}

// Bytes returns a sub-slice of r.B. Callers that retain it must clone.
func (r *Reader) Bytes() ([]byte, error) {
	n, err := r.Uvarint()
	if err != nil {
		return nil, err
	}
	end := r.I + int(n)
	if end > len(r.B) {
		return nil, ErrTruncated
	}
	b := r.B[r.I:end]
	r.I = end
	return b, nil
}

// String copies, which is why nothing decoded can alias the caller's buffer.
func (r *Reader) String() (string, error) {
	b, err := r.Bytes()
	if err != nil {
		return "", err
	}
	return string(b), nil
}
