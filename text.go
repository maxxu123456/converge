package converge

// Text is a collaborative string, indexed in runes (Unicode code points).
type Text struct {
	name    string
	start   *item // head of the list, tombstones included, nil when never written
	runeLen int   // visible runes
	byteLen int   // visible UTF-8 bytes
	u16Len  int   // visible UTF-16 code units
}
