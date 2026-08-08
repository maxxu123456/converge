package converge

import (
	"crypto/rand"
	"encoding/binary"
	"strconv"
)

// ClientID identifies a replica, minted from crypto/rand when a Doc is created.
// Zero is reserved as the "no client" half of the zero ID.
type ClientID uint64

// String renders the id in hex, for diagnostics only.
func (c ClientID) String() string { return strconv.FormatUint(uint64(c), 16) }

// ID names a single rune produced by one replica. The zero ID means "none" and
// is the sentinel used for an absent origin. ID is comparable and map-key safe.
type ID struct {
	Client ClientID
	Clock  uint64
}

// IsZero reports whether id is the "none" sentinel.
func (id ID) IsZero() bool { return id.Client == 0 }

// String renders "client:clock" in hex:decimal, for diagnostics only.
func (id ID) String() string {
	return id.Client.String() + ":" + strconv.FormatUint(id.Clock, 10)
}

// newClientID mints a non-zero identity. crypto/rand, never math/rand: seeded
// PRNGs across identical containers collide, and that is permanent divergence.
func newClientID() ClientID {
	var b [8]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			panic("converge: crypto/rand unavailable: " + err.Error())
		}
		if c := ClientID(binary.LittleEndian.Uint64(b[:])); c != 0 {
			return c
		}
	}
}
