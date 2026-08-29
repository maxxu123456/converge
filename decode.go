package converge

import (
	"slices"
	"sort"
	"unicode/utf8"

	"github.com/maxxu123456/converge/internal/wire"
)

// Smallest encodings of each section, used to reject a count before it can
// size a slice or a map.
const (
	minStructBytes       = 5 // info, an origin or a one-byte name, contentLen, one byte
	minRunBytes          = minStructBytes + 2
	minClientBytes       = minRunBytes + 2
	minDeleteClientBytes = 4
	minDeleteRangeBytes  = 2
)

// readHeader consumes the three-byte blob header, requiring kind. A version
// this build does not implement stops the decode before any further byte.
func readHeader(r *wire.Reader, kind byte, malformed error) error {
	off := r.I
	if r.Remaining() < 3 {
		return decodeErr(off, "header", malformed)
	}
	h := r.B[off : off+3]
	r.I = off + 3
	switch {
	case h[0] != blobMagic:
		return decodeErr(off, "header.magic", malformed)
	case h[1] != kind:
		return decodeErr(off+1, "header.kind", malformed)
	case h[2] != blobVersion:
		return decodeErr(off+2, "header.version", ErrUnsupportedVersion)
	}
	return nil
}

func badUpdate(off int, field string) *DecodeError {
	return decodeErr(off, field, ErrMalformedUpdate)
}

// decodeUpdate parses an update blob. Every string it returns is a copy, so
// nothing decoded aliases b.
func decodeUpdate(b []byte) (map[ClientID][]decoded, deleteSet, error) {
	r := &wire.Reader{B: b}
	if err := readHeader(r, kindUpdate, ErrMalformedUpdate); err != nil {
		return nil, nil, err
	}
	structs, err := readStructs(r)
	if err != nil {
		return nil, nil, err
	}
	ds, err := readDeleteSet(r)
	if err != nil {
		return nil, nil, err
	}
	if r.Remaining() != 0 {
		return nil, nil, badUpdate(r.I, "update.trailing")
	}
	return structs, ds, nil
}

func readStructs(r *wire.Reader) (map[ClientID][]decoded, error) {
	off := r.I
	n, err := r.Uvarint()
	if err != nil {
		return nil, badUpdate(off, "update.numClients")
	}
	if n > uint64(r.Remaining()/minClientBytes) {
		return nil, badUpdate(off, "update.numClients")
	}
	out := make(map[ClientID][]decoded, n)
	var prev ClientID
	for i := uint64(0); i < n; i++ {
		off := r.I
		c, err := readClientID(r, "clientSection.clientID")
		if err != nil {
			return nil, err
		}
		if i > 0 && c <= prev {
			return nil, badUpdate(off, "clientSection.clientID")
		}
		prev = c
		ss, err := readRuns(r, c)
		if err != nil {
			return nil, err
		}
		out[c] = ss
	}
	return out, nil
}

func readRuns(r *wire.Reader, c ClientID) ([]decoded, error) {
	off := r.I
	nr, err := r.Uvarint()
	if err != nil {
		return nil, badUpdate(off, "clientSection.numRuns")
	}
	if nr == 0 || nr > uint64(r.Remaining()/minRunBytes) {
		return nil, badUpdate(off, "clientSection.numRuns")
	}
	ss := make([]decoded, 0, nr)
	var prevEnd uint64
	for i := uint64(0); i < nr; i++ {
		off := r.I
		start, err := r.Uvarint()
		if err != nil {
			return nil, badUpdate(off, "run.startClock")
		}
		// a run abutting the one before it gives one state two encodings
		if i > 0 && start <= prevEnd {
			return nil, badUpdate(off, "run.startClock")
		}
		off = r.I
		ns, err := r.Uvarint()
		if err != nil {
			return nil, badUpdate(off, "run.numStructs")
		}
		if ns == 0 || ns > uint64(r.Remaining()/minStructBytes) {
			return nil, badUpdate(off, "run.numStructs")
		}
		clock := start
		for k := uint64(0); k < ns; k++ {
			off := r.I
			s, err := readStruct(r, c, clock)
			if err != nil {
				return nil, err
			}
			if s.endClock() < clock {
				return nil, badUpdate(off, "struct.clock")
			}
			clock = s.endClock()
			ss = append(ss, s)
		}
		prevEnd = clock
	}
	return ss, nil
}

func readStruct(r *wire.Reader, c ClientID, clock uint64) (decoded, error) {
	off := r.I
	info, err := r.Byte()
	if err != nil {
		return decoded{}, badUpdate(off, "struct.info")
	}
	if info&infoReserved != 0 {
		return decoded{}, badUpdate(off, "struct.info")
	}
	// a struct names its parent exactly when it has no neighbour to inherit it from
	if (info&infoHasParentName != 0) != (info&(infoHasOrigin|infoHasRightOrigin) == 0) {
		return decoded{}, badUpdate(off, "struct.info")
	}
	s := decoded{client: c, clock: clock}
	if info&infoHasOrigin != 0 {
		if s.origin, err = readOriginID(r, c, clock, "struct.origin"); err != nil {
			return decoded{}, err
		}
	}
	if info&infoHasRightOrigin != 0 {
		if s.rightOrigin, err = readOriginID(r, c, clock, "struct.rightOrigin"); err != nil {
			return decoded{}, err
		}
	}
	if info&infoHasParentName != 0 {
		off := r.I
		name, err := r.String()
		if err != nil {
			return decoded{}, badUpdate(off, "struct.name")
		}
		if name == "" || len(name) > 255 || !utf8.ValidString(name) {
			return decoded{}, badUpdate(off, "struct.name")
		}
		s.parentName = name
	}
	off = r.I
	content, err := r.String()
	if err != nil {
		return decoded{}, badUpdate(off, "struct.content")
	}
	if content == "" || !utf8.ValidString(content) {
		return decoded{}, badUpdate(off, "struct.content")
	}
	s.content = content
	s.runeLen = uint32(utf8.RuneCountInString(content))
	s.u16Len = utf16LenOf(content)
	return s, nil
}

func readClientID(r *wire.Reader, field string) (ClientID, error) {
	off := r.I
	v, err := r.Uvarint()
	if err != nil || v == 0 {
		return 0, badUpdate(off, field)
	}
	return ClientID(v), nil
}

// readOriginID reads a neighbour id. An item cannot depend on its own client's
// future, so such an id is rejected here and never reaches integration.
func readOriginID(r *wire.Reader, c ClientID, clock uint64, field string) (ID, error) {
	off := r.I
	oc, err := readClientID(r, field)
	if err != nil {
		return ID{}, err
	}
	at := r.I
	oclock, err := r.Uvarint()
	if err != nil {
		return ID{}, badUpdate(at, field)
	}
	if oc == c && oclock >= clock {
		return ID{}, badUpdate(off, field)
	}
	return ID{Client: oc, Clock: oclock}, nil
}

func badPosition(off int, field string) *DecodeError {
	return decodeErr(off, field, ErrMalformedPosition)
}

// readPosition parses a position blob whole. Trailing bytes are a failure, so
// one blob never decodes two ways.
func readPosition(b []byte) (Position, error) {
	r := &wire.Reader{B: b}
	if err := readHeader(r, kindPosition, ErrMalformedPosition); err != nil {
		return Position{}, err
	}
	off := r.I
	name, err := r.String()
	if err != nil || name == "" || len(name) > 255 || !utf8.ValidString(name) {
		return Position{}, badPosition(off, "position.name")
	}
	off = r.I
	kind, err := r.Byte()
	if err != nil || kind > byte(posAnchored) {
		return Position{}, badPosition(off, "position.kind")
	}
	off = r.I
	assoc, err := r.Byte()
	if err != nil || assoc > 1 {
		return Position{}, badPosition(off, "position.assoc")
	}
	p := Position{name: name, kind: posKind(kind)}
	if assoc == 1 {
		p.assoc = AssocBefore
	}
	if p.kind == posAnchored {
		off = r.I
		client, err := r.Uvarint()
		if err != nil || client == 0 {
			return Position{}, badPosition(off, "position.client")
		}
		off = r.I
		clock, err := r.Uvarint()
		if err != nil {
			return Position{}, badPosition(off, "position.clock")
		}
		p.item = ID{Client: ClientID(client), Clock: clock}
	}
	if r.Remaining() != 0 {
		return Position{}, badPosition(r.I, "position.trailing")
	}
	return p, nil
}

func readDeleteSet(r *wire.Reader) (deleteSet, error) {
	off := r.I
	n, err := r.Uvarint()
	if err != nil {
		return nil, badUpdate(off, "deleteSet.numClients")
	}
	if n > uint64(r.Remaining()/minDeleteClientBytes) {
		return nil, badUpdate(off, "deleteSet.numClients")
	}
	ds := make(deleteSet, n)
	var prev ClientID
	for i := uint64(0); i < n; i++ {
		off := r.I
		c, err := readClientID(r, "deleteSet.clientID")
		if err != nil {
			return nil, err
		}
		if i > 0 && c <= prev {
			return nil, badUpdate(off, "deleteSet.clientID")
		}
		prev = c
		rs, err := readRanges(r)
		if err != nil {
			return nil, err
		}
		ds[c] = rs
	}
	return ds, nil
}

func readRanges(r *wire.Reader) ([]idRange, error) {
	off := r.I
	n, err := r.Uvarint()
	if err != nil {
		return nil, badUpdate(off, "deleteSet.numRanges")
	}
	if n == 0 || n > uint64(r.Remaining()/minDeleteRangeBytes) {
		return nil, badUpdate(off, "deleteSet.numRanges")
	}
	rs := make([]idRange, 0, n)
	var prevEnd uint64
	for i := uint64(0); i < n; i++ {
		off := r.I
		gap, err := r.Uvarint()
		if err != nil {
			return nil, badUpdate(off, "deleteSet.gap")
		}
		// a zero gap after the first range abuts, and abutting ranges are two
		// encodings of one state
		if i > 0 && gap == 0 {
			return nil, badUpdate(off, "deleteSet.gap")
		}
		clock := prevEnd + gap
		if clock < prevEnd {
			return nil, badUpdate(off, "deleteSet.gap")
		}
		off = r.I
		length, err := r.Uvarint()
		if err != nil {
			return nil, badUpdate(off, "deleteSet.length")
		}
		if length == 0 || clock+length < clock {
			return nil, badUpdate(off, "deleteSet.length")
		}
		rs = append(rs, idRange{clock: clock, length: length})
		prevEnd = clock + length
	}
	return rs, nil
}

// validateUpdate rejects what no arrival order could integrate: a self-future
// origin, overlapping same-client ranges, or a cycle. It orders nothing.
func validateUpdate(structs map[ClientID][]decoded) error {
	clients := make([]ClientID, 0, len(structs))
	for c := range structs {
		clients = append(clients, c)
	}
	// node numbers must not depend on map order
	slices.Sort(clients)
	base := make(map[ClientID]int, len(structs))
	n := 0
	for _, c := range clients {
		base[c] = n
		n += len(structs[c])
	}

	for _, c := range clients {
		var prevEnd uint64
		for i, s := range structs[c] {
			// the decoder catches these as well, so that validateUpdate still
			// stands alone on a struct set that never came off the wire
			if selfFuture(s, s.origin) {
				return badUpdate(0, "struct.origin")
			}
			if selfFuture(s, s.rightOrigin) {
				return badUpdate(0, "struct.rightOrigin")
			}
			if i > 0 && s.clock < prevEnd {
				return badUpdate(0, "struct.clock")
			}
			prevEnd = s.endClock()
		}
	}

	edges := make([][]int, n)
	indeg := make([]int, n)
	for _, c := range clients {
		for i, s := range structs[c] {
			v := base[c] + i
			// nothing can go in before its own client's predecessor
			if i > 0 {
				edges[v-1] = append(edges[v-1], v)
				indeg[v]++
			}
			for _, o := range [2]ID{s.origin, s.rightOrigin} {
				if o.IsZero() {
					continue
				}
				if j := structAt(structs[o.Client], o.Clock); j >= 0 {
					u := base[o.Client] + j
					edges[u] = append(edges[u], v)
					indeg[v]++
				}
			}
		}
	}

	queue := make([]int, 0, n)
	for v := range indeg {
		if indeg[v] == 0 {
			queue = append(queue, v)
		}
	}
	done := 0
	for len(queue) > 0 {
		v := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		done++
		for _, w := range edges[v] {
			indeg[w]--
			if indeg[w] == 0 {
				queue = append(queue, w)
			}
		}
	}
	if done != n {
		return badUpdate(0, "struct.originCycle")
	}
	return nil
}

func selfFuture(s decoded, o ID) bool {
	return !o.IsZero() && o.Client == s.client && o.Clock >= s.clock
}

// structAt returns the index of the struct covering clock, or -1. ss must be
// ordered by clock.
func structAt(ss []decoded, clock uint64) int {
	i := sort.Search(len(ss), func(i int) bool { return ss[i].endClock() > clock })
	if i == len(ss) || ss[i].clock > clock {
		return -1
	}
	return i
}
