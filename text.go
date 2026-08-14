package converge

import "unicode/utf8"

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

func checkTextName(name string) {
	if name == "" || len(name) > 255 || !utf8.ValidString(name) {
		panic(&UsageError{Msg: "root name must be 1 to 255 bytes of valid UTF-8"})
	}
}
