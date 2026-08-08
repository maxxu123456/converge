package converge

import "github.com/maxxu123456/converge/internal/wire"

// readHeader consumes the three-byte blob header, requiring kind. A version
// this build does not implement stops the decode before any further byte.
func readHeader(r *wire.Reader, kind byte, malformed error) error {
	off := r.I
	if r.Remaining() < 3 {
		return decodeErr(off, "header", malformed)
	}
	h := r.B[off : off+3]
	r.I = off + 3
	switch {
	case h[0] != blobMagic:
		return decodeErr(off, "header.magic", malformed)
	case h[1] != kind:
		return decodeErr(off+1, "header.kind", malformed)
	case h[2] != blobVersion:
		return decodeErr(off+2, "header.version", ErrUnsupportedVersion)
	}
	return nil
}
