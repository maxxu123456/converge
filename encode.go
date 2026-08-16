package converge

import (
	"slices"
	"strings"

	"github.com/maxxu123456/converge/internal/wire"
)

const (
	blobMagic   byte = 0xCF
	blobVersion byte = 0x01
)

// Blob kinds, the header's second byte.
const (
	kindUpdate      byte = 0x01
	kindStateVector byte = 0x02
	kindAwareness   byte = 0x03
	kindPosition    byte = 0x04
)

// A struct's info byte. Bits 0x08 and up are reserved and must stay zero.
const (
	infoHasOrigin      byte = 0x01
	infoHasRightOrigin byte = 0x02
	infoHasParentName  byte = 0x04
	infoReserved       byte = 0xF8
)

// decoded is one struct in transit: the fields the wire carries plus the rune
// counts it leaves implicit. Content is always a copy, never a sub-slice.
type decoded struct {
	client      ClientID
	clock       uint64
	origin      ID
	rightOrigin ID
	parentName  string // set exactly when both origins are absent
	content     string
	runeLen     uint32
	u16Len      uint32
}

func (s decoded) endClock() uint64 { return s.clock + uint64(s.runeLen) }

// writeHeader writes the three-byte magic, kind and version prefix every
// standalone blob starts with.
func writeHeader(w *wire.Writer, kind byte) {
	w.Byte(blobMagic)
	w.Byte(kind)
	w.Byte(blobVersion)
}

// encodeStructs emits structs exactly as given, with no folding, so that
// re-encoding a decoded update reproduces the bytes it came from. Every
// client's slice must already be ordered by clock.
func encodeStructs(structs map[ClientID][]decoded, ds deleteSet) Update {
	return encodeUpdate(structs, ds, false)
}

// encodeCanonical folds each client's structs before emitting, so two replicas
// holding the same state produce the same bytes whatever their split history.
func encodeCanonical(structs map[ClientID][]decoded, ds deleteSet) Update {
	return encodeUpdate(structs, ds, true)
}

func encodeUpdate(structs map[ClientID][]decoded, ds deleteSet, fold bool) Update {
	w := &wire.Writer{B: make([]byte, 0, 64)}
	writeHeader(w, kindUpdate)
	clients := sortedClients(structs)
	w.Uvarint(uint64(len(clients)))
	for _, c := range clients {
		ss := structs[c]
		if fold {
			ss = foldRuns(ss)
		}
		w.Uvarint(uint64(c))
		writeRuns(w, ss)
	}
	writeDeleteSet(w, ds)
	return w.B
}

// foldRuns merges every maximal chain of clock-contiguous structs that splitAt
// would regenerate, so in-memory block boundaries never reach the wire.
func foldRuns(ss []decoded) []decoded {
	out := make([]decoded, 0, len(ss))
	for i := 0; i < len(ss); {
		s, nb, j := ss[i], len(ss[i].content), i+1
		for j < len(ss) && foldsInto(s, ss[j]) {
			s.runeLen += ss[j].runeLen
			s.u16Len += ss[j].u16Len
			nb += len(ss[j].content)
			j++
		}
		if j > i+1 {
			var b strings.Builder
			b.Grow(nb)
			for _, f := range ss[i:j] {
				b.WriteString(f.content)
			}
			s.content = b.String()
		}
		out = append(out, s)
		i = j
	}
	return out
}

// foldsInto reports whether next continues s exactly as a split would have cut
// it. List adjacency and deleted-ness are deliberately not part of the test.
func foldsInto(s, next decoded) bool {
	return next.clock == s.endClock() &&
		next.origin == ID{Client: s.client, Clock: next.clock - 1} &&
		next.rightOrigin == s.rightOrigin &&
		next.parentName == ""
}

// sortedClients returns the clients holding at least one struct, ascending.
// Map order must never reach the wire.
func sortedClients(structs map[ClientID][]decoded) []ClientID {
	cs := make([]ClientID, 0, len(structs))
	for c, ss := range structs {
		if len(ss) > 0 {
			cs = append(cs, c)
		}
	}
	slices.Sort(cs)
	return cs
}

// writeRuns cuts ss at its genuine clock gaps and emits one run per piece.
// Two runs are never adjacent, so the gaps alone rebuild them on decode.
func writeRuns(w *wire.Writer, ss []decoded) {
	n := 1
	for i := 1; i < len(ss); i++ {
		if ss[i].clock != ss[i-1].endClock() {
			n++
		}
	}
	w.Uvarint(uint64(n))
	start := 0
	for i := 1; i <= len(ss); i++ {
		if i < len(ss) && ss[i].clock == ss[i-1].endClock() {
			continue
		}
		run := ss[start:i]
		w.Uvarint(run[0].clock)
		w.Uvarint(uint64(len(run)))
		for _, s := range run {
			writeStruct(w, s)
		}
		start = i
	}
}

func writeStruct(w *wire.Writer, s decoded) {
	var info byte
	if !s.origin.IsZero() {
		info |= infoHasOrigin
	}
	if !s.rightOrigin.IsZero() {
		info |= infoHasRightOrigin
	}
	// a struct with a neighbour inherits its parent, so only an orphan names one
	if info == 0 {
		info = infoHasParentName
	}
	w.Byte(info)
	if info&infoHasOrigin != 0 {
		w.Uvarint(uint64(s.origin.Client))
		w.Uvarint(s.origin.Clock)
	}
	if info&infoHasRightOrigin != 0 {
		w.Uvarint(uint64(s.rightOrigin.Client))
		w.Uvarint(s.rightOrigin.Clock)
	}
	if info&infoHasParentName != 0 {
		w.String(s.parentName)
	}
	w.String(s.content)
}

// writeDeleteSet emits ds, which must already be normalized. The first range of
// a client carries an absolute clock, later ones the gap since the last end.
func writeDeleteSet(w *wire.Writer, ds deleteSet) {
	cs := make([]ClientID, 0, len(ds))
	for c, rs := range ds {
		if len(rs) > 0 {
			cs = append(cs, c)
		}
	}
	slices.Sort(cs)
	w.Uvarint(uint64(len(cs)))
	for _, c := range cs {
		w.Uvarint(uint64(c))
		rs := ds[c]
		w.Uvarint(uint64(len(rs)))
		var prevEnd uint64
		for i, r := range rs {
			if i == 0 {
				w.Uvarint(r.clock)
			} else {
				w.Uvarint(r.clock - prevEnd)
			}
			w.Uvarint(r.length)
			prevEnd = r.end()
		}
	}
}
