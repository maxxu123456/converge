package converge

import "github.com/maxxu123456/converge/internal/wire"

// Assoc says which side of a position a cursor sticks to.
type Assoc int8

const (
	// AssocAfter anchors to the rune after the index, so text inserted at the
	// index lands to the left of the cursor. This is the default.
	AssocAfter Assoc = 0
	// AssocBefore anchors to the rune before the index, so text inserted at
	// the index lands to the right of the cursor.
	AssocBefore Assoc = -1
)

type posKind uint8

const (
	posStart    posKind = 0 // before the first rune, forever
	posEnd      posKind = 1 // after the last rune, forever
	posAnchored posKind = 2
)

// Position is a sticky position in a Text: it names a rune, not an index, so
// concurrent edits elsewhere do not move it. The zero Position is invalid.
type Position struct {
	name  string // root Text name, empty only in the zero Position
	item  ID     // meaningful only when kind is posAnchored
	kind  posKind
	assoc Assoc
}

// Valid reports whether p names a Text.
func (p Position) Valid() bool { return p.name != "" }

// Assoc returns p's association.
func (p Position) Assoc() Assoc { return p.assoc }

// TextName returns the root Text name p refers to.
func (p Position) TextName() string { return p.name }

// MarshalBinary encodes p and satisfies encoding.BinaryMarshaler. It never
// fails, but the zero Position has no name and does not decode again.
func (p Position) MarshalBinary() ([]byte, error) {
	w := &wire.Writer{B: make([]byte, 0, 20)}
	writePosition(w, p)
	return w.B, nil
}

// UnmarshalBinary decodes bytes produced by MarshalBinary.
func (p *Position) UnmarshalBinary(b []byte) error {
	q, err := readPosition(b)
	if err != nil {
		return err
	}
	*p = q
	return nil
}

// anchorBase returns how far into it p's anchor sits, in visible runes.
func (p Position) anchorBase(it *item) int {
	if it.deleted {
		// a deleted anchor collapses to where its text was, with no +1, which
		// is what keeps a remote cursor still when the rune under it goes
		return 0
	}
	diff := int(p.item.Clock - it.id.Clock)
	if p.assoc == AssocBefore {
		return diff + 1
	}
	return diff
}
