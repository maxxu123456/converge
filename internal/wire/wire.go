// Package wire is the byte level of converge's blob formats: minimal-form
// LEB128 varints and length-prefixed bytes over a plain []byte.
package wire

import "encoding/binary"

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
