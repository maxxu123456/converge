package converge

// numMarkers is how many search markers a Text keeps.
const numMarkers = 8

// marker caches where one item sits, in visible runes and in UTF-16 code
// units. Enough of them turn a keystroke into a few steps instead of a walk.
type marker struct {
	it    *item
	rune  int    // visible rune index of it's first rune
	u16   int    // visible UTF-16 offset of it's first rune
	stamp uint64 // LRU clock, 0 when the slot is empty
}

// markerFor returns t's marker for it, or nil, and counts the hit.
func (t *Text) markerFor(it *item) *marker {
	for i := range t.markers {
		if m := &t.markers[i]; m.stamp != 0 && m.it == it {
			t.stamp++
			m.stamp = t.stamp
			return m
		}
	}
	return nil
}

// installMarker caches it at runeIdx and u16Idx, evicting the slot used
// longest ago.
func (t *Text) installMarker(it *item, runeIdx, u16Idx int) {
	slot := &t.markers[0]
	for i := range t.markers {
		m := &t.markers[i]
		if m.stamp != 0 && m.it == it {
			slot = m
			break
		}
		if m.stamp < slot.stamp {
			slot = m
		}
	}
	t.stamp++
	*slot = marker{it: it, rune: runeIdx, u16: u16Idx, stamp: t.stamp}
}

// clearMarkers forgets everything cached for t.
func (t *Text) clearMarkers() { t.markers = [numMarkers]marker{} }

// updateMarkerChanges shifts every cached index at or past runeIdx by a change
// of dRune visible runes, and forgets the ones a delete took with it.
func (t *Text) updateMarkerChanges(runeIdx, dRune, dU16 int) {
	for i := range t.markers {
		m := &t.markers[i]
		if m.stamp == 0 || m.rune < runeIdx {
			continue
		}
		if dRune < 0 && m.rune < runeIdx-dRune {
			*m = marker{}
			continue
		}
		m.rune += dRune
		m.u16 += dU16
	}
}

// dropMarkersOn forgets every marker on it.
func (t *Text) dropMarkersOn(it *item) {
	for i := range t.markers {
		if m := &t.markers[i]; m.stamp != 0 && m.it == it {
			*m = marker{}
		}
	}
}

// visibleIndexOf returns the visible rune and UTF-16 offsets of it's first
// rune, walking left until a cached marker answers.
func visibleIndexOf(it *item) (runeIdx, u16Idx int) {
	t := it.parent
	for p := it.left; p != nil; p = p.left {
		// p's runes count first: a marker says where p starts, not where it ends
		if !p.deleted {
			runeIdx += int(p.runeLen)
			u16Idx += int(p.u16Len)
		}
		if m := t.markerFor(p); m != nil {
			return m.rune + runeIdx, m.u16 + u16Idx
		}
	}
	return runeIdx, u16Idx
}

// repointMarker moves any marker on from onto to, shifting its cached offsets.
// The merge pass calls it while from's runes are moving into to.
func repointMarker(from, to *item, dRune, dU16 int) {
	t := from.parent
	for i := range t.markers {
		m := &t.markers[i]
		if m.stamp == 0 || m.it != from {
			continue
		}
		m.it = to
		m.rune += dRune
		m.u16 += dU16
	}
}
