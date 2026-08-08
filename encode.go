package converge

import "github.com/maxxu123456/converge/internal/wire"

const (
	blobMagic   byte = 0xCF
	blobVersion byte = 0x01
)

// Blob kinds, the header's second byte.
const (
	kindUpdate      byte = 0x01
	kindStateVector byte = 0x02
	kindAwareness   byte = 0x03
	kindPosition    byte = 0x04
)

// writeHeader writes the three-byte magic, kind and version prefix every
// standalone blob starts with.
func writeHeader(w *wire.Writer, kind byte) {
	w.Byte(blobMagic)
	w.Byte(kind)
	w.Byte(blobVersion)
}
