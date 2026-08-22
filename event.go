package converge

import "strconv"

// Delta is one operation of an editor delta. Exactly one field is non-zero,
// and Retain and Delete count runes.
type Delta struct {
	Retain int
	Insert string
	Delete int
}

// String renders the op compactly, for diagnostics and test failures.
func (d Delta) String() string {
	switch {
	case d.Insert != "":
		return "insert " + strconv.Quote(d.Insert)
	case d.Delete > 0:
		return "delete " + strconv.Itoa(d.Delete)
	case d.Retain > 0:
		return "retain " + strconv.Itoa(d.Retain)
	}
	return "noop"
}

// Event describes one committed change to one Text. The Delta slice belongs to
// the receiver, converge never reads or mutates it again.
type Event struct {
	Text   *Text
	Delta  []Delta
	Origin any  // whatever was passed to Transact or ApplyUpdate
	Local  bool // true for a Transact, false for an ApplyUpdate
}
