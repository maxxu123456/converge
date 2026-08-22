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

// observer is one registered callback. Registrations carry an id because Go
// funcs are not comparable, so cancel could not otherwise find its own binding.
type observer[F any] struct {
	fn F
	id uint64
}

// addObserver registers fn in *list and returns the func that removes it. The
// caller holds the document lock, the returned func takes it.
func addObserver[F any](d *Doc, list *[]*observer[F], fn F) (cancel func()) {
	d.nextObsID++
	o := &observer[F]{fn: fn, id: d.nextObsID}
	*list = append(*list, o)
	return func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		for i, x := range *list {
			if x.id == o.id {
				// a fresh array, so a dispatch already under way is untouched
				*list = append((*list)[:i:i], (*list)[i+1:]...)
				return
			}
		}
	}
}
