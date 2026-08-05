package converge

import "strconv"

// ClientID identifies a replica. It is minted from crypto/rand when a Doc is
// created. Zero is reserved: it is the "no client" half of the zero ID.
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
