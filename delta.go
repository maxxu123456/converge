package converge

import "slices"

// deltasFor builds one Event per changed Text that somebody is watching, in
// ascending name order. The caller holds the document lock.
func deltasFor(tx *Tx) []Event {
	d := tx.doc
	var names []string
	for name, t := range d.roots {
		if len(t.obs) > 0 { // a Text nobody watches is never walked
			names = append(names, name)
		}
	}
	slices.Sort(names)
	var events []Event
	for _, name := range names {
		t := d.roots[name]
		if ops := deltaFor(tx, t); len(ops) > 0 {
			events = append(events, Event{Text: t, Delta: ops, Origin: tx.origin, Local: tx.local})
		}
	}
	return events
}

// deltaFor walks t once and classifies every run against the clocks the
// transaction started from. Splits only subdivide a run, so each one is either
// wholly new or wholly old and the classification is exact.
func deltaFor(tx *Tx, t *Text) []Delta {
	var ops []Delta
	retain := 0
	flush := func() {
		if retain > 0 {
			ops = append(ops, Delta{Retain: retain})
			retain = 0
		}
	}
	for it := t.start; it != nil; it = it.right {
		isNew := it.id.Clock >= tx.before[it.id.Client] // an unknown client gives 0
		switch {
		case !it.deleted && isNew:
			flush()
			if n := len(ops); n > 0 && ops[n-1].Insert != "" {
				ops[n-1].Insert += it.content
			} else {
				ops = append(ops, Delta{Insert: it.content})
			}
		case !it.deleted && !isNew:
			retain += int(it.runeLen)
		case it.deleted && !isNew && tx.deleted.covers(it.id, it.runeLen):
			flush()
			if n := len(ops); n > 0 && ops[n-1].Delete > 0 {
				ops[n-1].Delete += int(it.runeLen)
			} else {
				ops = append(ops, Delta{Delete: int(it.runeLen)})
			}
		}
		// an older tombstone, or text both inserted and deleted here, says nothing
	}
	return ops // never flushing at the end is what drops the trailing retain
}
