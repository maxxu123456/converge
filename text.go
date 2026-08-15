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
}

// Name returns the root name this Text was registered under.
func (t *Text) Name() string { return t.name }

// Len returns the visible length in runes. Do not call it from inside a
// Transact callback, use Tx.Len.
func (t *Text) Len() int {
	t.doc.mu.Lock()
	defer t.doc.mu.Unlock()
	return t.runeLen
}

// String returns the visible text and satisfies fmt.Stringer. Do not call it
// from inside a Transact callback, use Tx.String.
func (t *Text) String() string {
	t.doc.mu.Lock()
	defer t.doc.mu.Unlock()
	return t.visible()
}

// Slice returns the runes in [start, end).
func (t *Text) Slice(start, end int) string {
	t.doc.mu.Lock()
	defer t.doc.mu.Unlock()
	return t.visibleSlice(start, end)
}

// WriteTo writes the visible text to w and satisfies io.WriterTo. It never
// holds the document lock while writing.
func (t *Text) WriteTo(w io.Writer) (int64, error) {
	t.doc.mu.Lock()
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
	t.doc.mu.Lock()
	defer t.doc.mu.Unlock()
	return t.u16Len
}

// UTF16Index converts a rune index in [0, Len()] to a UTF-16 code-unit offset.
func (t *Text) UTF16Index(runeIndex int) int {
	t.doc.mu.Lock()
	defer t.doc.mu.Unlock()
	return t.utf16Index(runeIndex)
}

// RuneIndex converts a UTF-16 code-unit offset to a rune index. An offset
// inside a surrogate pair rounds down to that pair, one past the end clamps.
func (t *Text) RuneIndex(utf16Index int) int {
	t.doc.mu.Lock()
	defer t.doc.mu.Unlock()
	return t.runeIndex(utf16Index)
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

// utf16Index returns the UTF-16 offset of visible rune index. The caller holds
// the lock.
func (t *Text) utf16Index(index int) int {
	r, u := 0, 0
	for it := t.start; it != nil && r < index; it = it.right {
		if it.deleted {
			continue
		}
		l := int(it.runeLen)
		if r+l <= index {
			r += l
			u += int(it.u16Len)
			continue
		}
		off := utf8ByteOffset(it.content, uint32(index-r))
		return u + int(utf16LenOf(it.content[:off]))
	}
	return u
}

// runeIndex returns the rune index of a UTF-16 offset. The caller holds the lock.
func (t *Text) runeIndex(u16 int) int {
	if u16 <= 0 {
		return 0
	}
	if u16 >= t.u16Len {
		return t.runeLen
	}
	r, u := 0, 0
	for it := t.start; it != nil; it = it.right {
		if it.deleted {
			continue
		}
		if u+int(it.u16Len) <= u16 {
			r += int(it.runeLen)
			u += int(it.u16Len)
			continue
		}
		for _, c := range it.content {
			w := 1
			if c > 0xFFFF {
				w = 2
			}
			if u+w > u16 {
				return r // an offset inside a surrogate pair rounds down
			}
			r++
			u += w
		}
	}
	return r
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
