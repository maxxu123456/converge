package converge

import "testing"

// Validate re-exports the invariant check, which the external tests also run.
func Validate(t testing.TB, d *Doc) {
	t.Helper()
	validate(t, d)
}

// forceSplit shatters every run into single-rune blocks, so a test can check
// that nothing a reader sees depends on where the block boundaries fall.
func forceSplit(s *structStore) {
	for _, cb := range s.clients {
		for i := 0; i < len(cb.blocks); i++ {
			if it := cb.blocks[i]; it.runeLen > 1 {
				s.splitAt(it, 1)
			}
		}
	}
}

func itemCount(s *structStore) int {
	n := 0
	for _, cb := range s.clients {
		n += len(cb.blocks)
	}
	return n
}
