package converge

import (
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
}

// Name returns the root name this Text was registered under.
func (t *Text) Name() string { return t.name }

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
	n := 0
	for it := t.start; it != nil && n < end; it = it.right {
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

// findVisible returns the item holding visible rune index and the rune offset
// of index within it. index must be below t.runeLen.
func (t *Text) findVisible(index int) (*item, int) {
	n := 0
	for it := t.start; it != nil; it = it.right {
		if it.deleted {
			continue
		}
		if index < n+int(it.runeLen) {
			return it, index - n
		}
		n += int(it.runeLen)
	}
	return nil, 0
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

func checkTextName(name string) {
	if name == "" || len(name) > 255 || !utf8.ValidString(name) {
		panic(&UsageError{Msg: "root name must be 1 to 255 bytes of valid UTF-8"})
	}
}
