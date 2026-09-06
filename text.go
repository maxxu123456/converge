package converge

import (
	"io"
	"strings"
	"unicode/utf8"
)

// Text is a collaborative string, indexed in runes (Unicode code points).
// Handles are stable: doc.Text("body") always returns the same *Text.
type Text struct {
	doc     *Doc
	name    string
	start   *item // head of the list, tombstones included, nil when never written
	runeLen int   // visible runes
	byteLen int   // visible UTF-8 bytes
	u16Len  int   // visible UTF-16 code units
	markers [numMarkers]marker
	stamp   uint64 // the markers' LRU clock
	obs     []*observer[func(Event)]
}

// Observe registers fn, called after each committed transaction that changed t.
// fn runs with the document lock released, in commit order. cancel unregisters it.
func (t *Text) Observe(fn func(Event)) (cancel func()) {
	t.doc.lock()
	defer t.doc.mu.Unlock()
	return addObserver(t.doc, &t.obs, fn)
}

// Name returns the root name this Text was registered under.
func (t *Text) Name() string { return t.name }

// Len returns the visible length in runes. Do not call it from inside a
// Transact callback, use Tx.Len.
func (t *Text) Len() int {
	t.doc.lock()
	defer t.doc.mu.Unlock()
	return t.runeLen
}

// String returns the visible text and satisfies fmt.Stringer. Do not call it
// from inside a Transact callback, use Tx.String.
func (t *Text) String() string {
	t.doc.lock()
	defer t.doc.mu.Unlock()
	return t.visible()
}

// Slice returns the runes in [start, end). Panics with *RangeError out of range.
func (t *Text) Slice(start, end int) string {
	t.doc.lock()
	defer t.doc.mu.Unlock()
	checkSlice(t, start, end)
	return t.visibleSlice(start, end)
}

// WriteTo writes the visible text to w and satisfies io.WriterTo. It never
// holds the document lock while writing.
func (t *Text) WriteTo(w io.Writer) (int64, error) {
	t.doc.lock()
	parts := make([]string, 0, 16)
	for it := t.start; it != nil; it = it.right {
		if !it.deleted {
			parts = append(parts, it.content)
		}
	}
	t.doc.mu.Unlock()
	// content is immutable, so the snapshot outlives the lock
	var n int64
	for _, p := range parts {
		m, err := io.WriteString(w, p)
		n += int64(m)
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// UTF16Len returns the visible length in UTF-16 code units, which is what a
// browser editor counts.
func (t *Text) UTF16Len() int {
	t.doc.lock()
	defer t.doc.mu.Unlock()
	return t.u16Len
}

// UTF16Index converts a rune index in [0, Len()] to a UTF-16 code-unit offset.
// Panics with *RangeError out of range.
func (t *Text) UTF16Index(runeIndex int) int {
	t.doc.lock()
	defer t.doc.mu.Unlock()
	if runeIndex < 0 || runeIndex > t.runeLen {
		panic(rangeErr(t, runeIndex, 0))
	}
	return t.utf16Index(runeIndex)
}

// RuneIndex converts a UTF-16 code-unit offset to a rune index. An offset
// inside a surrogate pair rounds down to that pair, one past the end clamps.
func (t *Text) RuneIndex(utf16Index int) int {
	t.doc.lock()
	defer t.doc.mu.Unlock()
	return t.runeIndex(utf16Index)
}

// Position returns a sticky anchor for index that survives concurrent edits by
// other replicas. index is clamped to [0, Len()], so Position never fails.
func (t *Text) Position(index int, assoc Assoc) Position {
	t.doc.lock()
	defer t.doc.mu.Unlock()
	if assoc != AssocBefore {
		// one value per side, so == still answers "did that cursor move?"
		assoc = AssocAfter
	}
	index = min(max(index, 0), t.runeLen)
	switch {
	case assoc == AssocBefore && index == 0:
		return Position{name: t.name, kind: posStart, assoc: assoc}
	case assoc == AssocAfter && index == t.runeLen:
		return Position{name: t.name, kind: posEnd, assoc: assoc}
	case assoc == AssocBefore:
		return t.anchorAt(index-1, assoc)
	default:
		return t.anchorAt(index, assoc)
	}
}

// anchorAt returns a position on the rune at visible index i, which must be
// below t.runeLen. The caller holds the document lock.
func (t *Text) anchorAt(i int, assoc Assoc) Position {
	it, off := t.findVisible(i)
	return Position{
		name:  t.name,
		item:  ID{Client: it.id.Client, Clock: it.id.Clock + uint64(off)},
		kind:  posAnchored,
		assoc: assoc,
	}
}

// visible returns t's visible content. The caller holds the document lock.
func (t *Text) visible() string {
	var b strings.Builder
	b.Grow(t.byteLen)
	for it := t.start; it != nil; it = it.right {
		if !it.deleted {
			b.WriteString(it.content)
		}
	}
	return b.String()
}

// visibleSlice returns the visible runes in [start, end). The caller holds the lock.
func (t *Text) visibleSlice(start, end int) string {
	var b strings.Builder
	it, n, _ := t.seek(start)
	for ; it != nil && n < end; it = it.right {
		if it.deleted {
			continue
		}
		l := int(it.runeLen)
		if n+l > start {
			from, to := 0, l
			if start-n > from {
				from = start - n
			}
			if end-n < to {
				to = end - n
			}
			lo := utf8ByteOffset(it.content, uint32(from))
			b.WriteString(it.content[lo:utf8ByteOffset(it.content, uint32(to))])
		}
		n += l
	}
	return b.String()
}

// utf16Index returns the UTF-16 offset of visible rune index. The caller holds
// the lock.
func (t *Text) utf16Index(index int) int {
	it, r, u := t.seek(index)
	if it == nil {
		return t.u16Len
	}
	off := utf8ByteOffset(it.content, uint32(index-r))
	return u + int(utf16LenOf(it.content[:off]))
}

// runeIndex returns the rune index of a UTF-16 offset. The caller holds the lock.
func (t *Text) runeIndex(u16 int) int {
	if u16 <= 0 {
		return 0
	}
	if u16 >= t.u16Len {
		return t.runeLen
	}
	it, r, u := t.seekU16(u16)
	if it == nil {
		return t.runeLen
	}
	for _, c := range it.content {
		w := 1
		if c > 0xFFFF {
			w = 2
		}
		if u+w > u16 {
			break // an offset inside a surrogate pair rounds down
		}
		r++
		u += w
	}
	return r
}

// nearest returns the cached waypoint closest to want, measured in UTF-16 code
// units when byU16 is set, or the head of the list.
func (t *Text) nearest(want int, byU16 bool) (it *item, r, u int) {
	it, best := t.start, want
	for i := range t.markers {
		m := &t.markers[i]
		if m.stamp == 0 {
			continue
		}
		at := m.rune
		if byU16 {
			at = m.u16
		}
		d := at - want
		if d < 0 {
			d = -d
		}
		if d < best {
			it, r, u, best = m.it, m.rune, m.u16, d
		}
	}
	return it, r, u
}

// seek returns the item holding visible rune index, with the offsets of that
// item's first rune. it is nil once index is at or past the end.
func (t *Text) seek(index int) (*item, int, int) {
	it, r, u := t.nearest(index, false)
	for it != nil && r > index {
		it = it.left
		if it != nil && !it.deleted {
			r -= int(it.runeLen)
			u -= int(it.u16Len)
		}
	}
	for it != nil && (it.deleted || r+int(it.runeLen) <= index) {
		if !it.deleted {
			r += int(it.runeLen)
			u += int(it.u16Len)
		}
		it = it.right
	}
	if it != nil {
		t.installMarker(it, r, u)
	}
	return it, r, u
}

// seekU16 is seek keyed on a UTF-16 offset instead of a rune index.
func (t *Text) seekU16(off int) (*item, int, int) {
	it, r, u := t.nearest(off, true)
	for it != nil && u > off {
		it = it.left
		if it != nil && !it.deleted {
			r -= int(it.runeLen)
			u -= int(it.u16Len)
		}
	}
	for it != nil && (it.deleted || u+int(it.u16Len) <= off) {
		if !it.deleted {
			r += int(it.runeLen)
			u += int(it.u16Len)
		}
		it = it.right
	}
	if it != nil {
		t.installMarker(it, r, u)
	}
	return it, r, u
}

// findVisible returns the item holding visible rune index and the rune offset
// of index within it. index must be below t.runeLen.
func (t *Text) findVisible(index int) (*item, int) {
	it, r, _ := t.seek(index)
	if it == nil {
		return nil, 0
	}
	return it, index - r
}

// findInsertPos returns the neighbours a new item inserted before visible rune
// index is born with, splitting the item on the left when index falls inside it.
func (t *Text) findInsertPos(index int) (left, right *item) {
	if index == 0 {
		return nil, t.start
	}
	it, off := t.findVisible(index - 1)
	if off+1 < int(it.runeLen) {
		t.doc.store.splitAt(it, uint32(off+1))
	}
	// it now ends at visible rune index-1, and its right may be a tombstone
	return it, it.right
}

func checkSlice(t *Text, start, end int) {
	if start < 0 || end < start || end > t.runeLen {
		panic(rangeErr(t, start, end-start))
	}
}

func checkTextName(name string) {
	if name == "" || len(name) > 255 || !utf8.ValidString(name) {
		panic(&UsageError{Msg: "root name must be 1 to 255 bytes of valid UTF-8"})
	}
}
