package converge

import (
	"bytes"
	"errors"
	"fmt"
	"unicode/utf8"
)

// checkInvariants reports the first structural rule this document breaks. No
// library code calls it. Tests call it after every operation.
func (d *Doc) checkInvariants() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := checkBlocks(&d.store); err != nil {
		return err
	}
	pos, err := d.checkLists()
	if err != nil {
		return err
	}
	for _, cb := range d.store.clients {
		for _, it := range cb.blocks {
			if err := checkAnchors(&d.store, pos, it); err != nil {
				return err
			}
		}
	}
	for _, t := range d.roots {
		if err := checkMarkers(t); err != nil {
			return err
		}
	}
	if err := checkCanonicalDeleteSet(deleteSetFromStore(&d.store)); err != nil {
		return err
	}
	if err := d.checkPending(); err != nil {
		return err
	}
	return d.checkEncoding()
}

// checkBlocks asserts each client's runs cover [0, next) ascending with no gap
// and no overlap, and that every run agrees with its own content.
func checkBlocks(st *structStore) error {
	for c, cb := range st.clients {
		var clock uint64
		for _, it := range cb.blocks {
			if it.id.Client != c {
				return fmt.Errorf("client %s holds a block of client %s", c, it.id.Client)
			}
			if it.id.Clock != clock {
				return fmt.Errorf("client %s has a block at clock %d where %d was due", c, it.id.Clock, clock)
			}
			if err := checkRun(it); err != nil {
				return err
			}
			clock = it.endClock()
		}
		if cb.next != clock {
			return fmt.Errorf("client %s ends at clock %d but next says %d", c, clock, cb.next)
		}
	}
	return nil
}

func checkRun(it *item) error {
	switch {
	case it.runeLen == 0:
		return fmt.Errorf("item %v holds no runes", it.id)
	case it.parent == nil:
		return fmt.Errorf("item %v has no parent", it.id)
	case !utf8.ValidString(it.content):
		return fmt.Errorf("item %v holds invalid UTF-8", it.id)
	case uint32(utf8.RuneCountInString(it.content)) != it.runeLen:
		return fmt.Errorf("item %v holds %d runes, runeLen says %d",
			it.id, utf8.RuneCountInString(it.content), it.runeLen)
	case utf16LenOf(it.content) != it.u16Len:
		return fmt.Errorf("item %v holds %d code units, u16Len says %d",
			it.id, utf16LenOf(it.content), it.u16Len)
	}
	return nil
}

// checkLists walks every root and returns each item's position in its list. It
// asserts the links, the reachable set and the three visible counters.
func (d *Doc) checkLists() (map[*item]int, error) {
	pos := make(map[*item]int)
	for _, t := range d.roots {
		if t.start != nil && t.start.left != nil {
			return nil, fmt.Errorf("root %q starts at %v, which has a predecessor", t.name, t.start.id)
		}
		var prev *item
		runes, byteLen, u16 := 0, 0, 0
		for it := t.start; it != nil; it = it.right {
			if _, twice := pos[it]; twice {
				return nil, fmt.Errorf("root %q reaches item %v twice", t.name, it.id)
			}
			pos[it] = len(pos)
			if it.left != prev {
				return nil, fmt.Errorf("item %v does not point back at its predecessor", it.id)
			}
			if it.parent != t {
				return nil, fmt.Errorf("item %v is listed in root %q", it.id, t.name)
			}
			if got := d.store.get(it.id); got != it {
				return nil, fmt.Errorf("the store answers %v with another item", it.id)
			}
			if !it.deleted {
				runes += int(it.runeLen)
				byteLen += len(it.content)
				u16 += int(it.u16Len)
			}
			prev = it
		}
		if runes != t.runeLen || byteLen != t.byteLen || u16 != t.u16Len {
			return nil, fmt.Errorf("root %q counts %d runes %d bytes %d units, its list holds %d %d %d",
				t.name, t.runeLen, t.byteLen, t.u16Len, runes, byteLen, u16)
		}
	}
	for _, cb := range d.store.clients {
		for _, it := range cb.blocks {
			if _, listed := pos[it]; !listed {
				return nil, fmt.Errorf("item %v is in the store but not in root %q", it.id, it.parent.name)
			}
		}
	}
	return pos, nil
}

// checkAnchors asserts both of it's anchors are held, sit in the same root and
// fall on the side of it they name.
func checkAnchors(st *structStore, pos map[*item]int, it *item) error {
	for _, a := range [2]ID{it.origin, it.rightOrigin} {
		if a.IsZero() {
			continue
		}
		if st.stateOf(a.Client) <= a.Clock {
			return fmt.Errorf("item %v names anchor %v, which this replica does not hold", it.id, a)
		}
		if st.get(a).parent != it.parent {
			return fmt.Errorf("item %v names anchor %v in another root", it.id, a)
		}
	}
	if o := st.get(it.origin); o != nil && pos[o] >= pos[it] {
		return fmt.Errorf("item %v has its origin %v at or to its right", it.id, o.id)
	}
	if r := st.get(it.rightOrigin); r != nil && pos[r] <= pos[it] {
		return fmt.Errorf("item %v has its rightOrigin %v at or to its left", it.id, r.id)
	}
	return nil
}

// checkMarkers asserts every cached marker still sits in t's list and still
// names the index a fresh walk gives it.
func checkMarkers(t *Text) error {
	live := 0
	for i := range t.markers {
		if t.markers[i].stamp != 0 {
			live++
		}
	}
	found, r, u := 0, 0, 0
	for it := t.start; it != nil; it = it.right {
		for i := range t.markers {
			m := &t.markers[i]
			if m.stamp == 0 || m.it != it {
				continue
			}
			found++
			if m.rune != r || m.u16 != u {
				return fmt.Errorf("root %q caches item %v at rune %d unit %d, the walk puts it at %d and %d",
					t.name, it.id, m.rune, m.u16, r, u)
			}
		}
		if !it.deleted {
			r += int(it.runeLen)
			u += int(it.u16Len)
		}
	}
	if found != live {
		return fmt.Errorf("root %q caches %d markers but only %d of them are in its list", t.name, live, found)
	}
	return nil
}

// checkCanonicalDeleteSet asserts ds is sorted, non-overlapping and non-adjacent
// per client, which is what makes it a function of the state alone.
func checkCanonicalDeleteSet(ds deleteSet) error {
	for c, rs := range ds {
		if len(rs) == 0 {
			return fmt.Errorf("client %s has an empty range list", c)
		}
		for i, r := range rs {
			if r.length == 0 {
				return fmt.Errorf("client %s has an empty range at clock %d", c, r.clock)
			}
			if i > 0 && r.clock <= rs[i-1].end() {
				return fmt.Errorf("client %s has a range at clock %d that touches the one before it", c, r.clock)
			}
		}
	}
	return nil
}

// checkPending asserts the causal backlog is ordered and counted, that nothing
// in it is already integrated, and that missing names what would unblock it.
func (d *Doc) checkPending() error {
	n := 0
	for c, ss := range d.pending {
		for i, s := range ss {
			switch {
			case s.client != c:
				return fmt.Errorf("client %s buffers a struct of client %s", c, s.client)
			case i > 0 && s.clock < ss[i-1].clock:
				return fmt.Errorf("client %s buffers clock %d after clock %d", c, s.clock, ss[i-1].clock)
			case s.endClock() <= d.store.stateOf(c):
				return fmt.Errorf("client %s buffers clock %d, which the store already holds", c, s.clock)
			}
			n++
		}
	}
	if n != d.pendingCount {
		return fmt.Errorf("the buffer holds %d structs but the count says %d", n, d.pendingCount)
	}
	for c, rs := range d.pendingDS {
		for _, r := range rs {
			if r.clock < d.store.stateOf(c) {
				return fmt.Errorf("client %s buffers a delete at clock %d, below the %d it holds",
					c, r.clock, d.store.stateOf(c))
			}
		}
	}
	want := d.missingClocks()
	if len(want) != len(d.missing) {
		return fmt.Errorf("missing names %d clients, the buffer blocks on %d", len(d.missing), len(want))
	}
	for c, clock := range want {
		if d.missing[c] != clock {
			return fmt.Errorf("missing says client %s from clock %d, the buffer needs %d",
				c, d.missing[c], clock)
		}
	}
	return nil
}

// checkEncoding asserts the canonical encoding depends on the state and on
// nothing else: the same bytes twice, and the same bytes through a round trip.
func (d *Doc) checkEncoding() error {
	u := d.encodeAll()
	if !bytes.Equal(u, d.encodeAll()) {
		return errors.New("two encodes of one state differ")
	}
	return checkCanonicalUpdate(u)
}

// encodeAll encodes everything this replica holds. The caller holds the lock.
func (d *Doc) encodeAll() Update {
	return encodeCanonical(structsSince(&d.store, nil), deleteSetFromStore(&d.store))
}

func checkCanonicalUpdate(u Update) error {
	structs, ds, err := decodeUpdate(u)
	if err != nil {
		return fmt.Errorf("the encoded state does not decode: %w", err)
	}
	if !bytes.Equal(encodeCanonical(structs, ds), u) {
		return errors.New("the encoded state is not canonical")
	}
	return nil
}
