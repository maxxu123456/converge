package converge

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

// Position is a sticky position in a Text. It names a rune rather than an
// index, so concurrent edits elsewhere do not move it. The zero Position is
// invalid.
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
