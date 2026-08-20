package converge

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// modelItem is one rune. The oracle keeps no runs, so nothing is ever split or
// merged and every id names exactly one item.
type modelItem struct {
	id          ID
	origin      ID
	rightOrigin ID
	content     string
	deleted     bool
}

// model is a second YATA implementation, deliberately naive: one item per rune,
// a linear scan to integrate, a map to find, one root and no store. It exists
// to separate "the run machinery is faithful" from "the algorithm is right".
type model struct {
	items []*modelItem
	pos   map[ID]int
	seen  map[ClientID]uint64
}

func newModel() *model {
	return &model{pos: map[ID]int{}, seen: map[ClientID]uint64{}}
}

// apply integrates every struct whose anchors are present, then marks the
// delete set, which is the order ApplyUpdate uses too.
func (m *model) apply(u Update) error {
	structs, ds, err := decodeUpdate(u)
	if err != nil {
		return err
	}
	for progress := true; progress; {
		progress = false
		for _, c := range sortedClients(structs) {
			ss := structs[c]
			for len(ss) > 0 && m.ready(ss[0]) {
				if err := m.insert(ss[0]); err != nil {
					return err
				}
				ss, progress = ss[1:], true
			}
			structs[c] = ss
		}
	}
	for c, rs := range ds {
		for _, r := range rs {
			for clock := r.clock; clock < r.end(); clock++ {
				if i, held := m.pos[ID{Client: c, Clock: clock}]; held {
					m.items[i].deleted = true
				}
			}
		}
	}
	return nil
}

func (m *model) ready(s decoded) bool {
	return s.clock <= m.seen[s.client] && m.held(s.origin) && m.held(s.rightOrigin)
}

func (m *model) held(id ID) bool {
	if id.IsZero() {
		return true
	}
	_, ok := m.pos[id]
	return ok
}

// insert explodes a run into one item per rune the way repeated splits would:
// every rune after the first takes the one before it as its origin.
func (m *model) insert(s decoded) error {
	origin := s.origin
	for i, r := range []rune(s.content) {
		id := ID{Client: s.client, Clock: s.clock + uint64(i)}
		if _, dup := m.pos[id]; !dup {
			it := &modelItem{id: id, origin: origin, rightOrigin: s.rightOrigin, content: string(r)}
			if err := m.integrate(it); err != nil {
				return err
			}
		}
		origin = id
	}
	if e := s.endClock(); e > m.seen[s.client] {
		m.seen[s.client] = e
	}
	return nil
}

// integrate walks the window between the two anchors: a direct conflict breaks
// on client id, and an item descending from something already settled goes
// before us. dest is where we land, conf where the still contested run starts.
func (m *model) integrate(it *modelItem) error {
	start, end := 0, len(m.items)
	if !it.origin.IsZero() {
		start = m.pos[it.origin] + 1
	}
	if !it.rightOrigin.IsZero() {
		end = m.pos[it.rightOrigin]
	}
	if end < start {
		return fmt.Errorf("model: %v has its origins out of order", it.id)
	}
	dest, conf := start, start
scan:
	for i := start; i < end; i++ {
		o := m.items[i]
		switch {
		case o.origin == it.origin:
			if o.id.Client < it.id.Client {
				dest, conf = i+1, i+1
			} else if o.rightOrigin == it.rightOrigin {
				break scan // same interval and we lost the tie, o is our right neighbour
			}
		case !o.origin.IsZero():
			oo, held := m.pos[o.origin]
			if !held || oo < start {
				break scan // o hangs off something outside the window
			}
			if oo < conf {
				dest, conf = i+1, i+1
			}
		default:
			break scan // o has no origin and is not our conflict peer
		}
	}
	m.items = slices.Insert(m.items, dest, it)
	for j := dest; j < len(m.items); j++ {
		m.pos[m.items[j].id] = j
	}
	return nil
}

func (m *model) text() string {
	var b strings.Builder
	for _, it := range m.items {
		if !it.deleted {
			b.WriteString(it.content)
		}
	}
	return b.String()
}

// modelText replays ups on a fresh oracle and returns what it reads.
func modelText(t *testing.T, ups ...Update) string {
	t.Helper()
	m := newModel()
	for i, u := range ups {
		if err := m.apply(u); err != nil {
			t.Fatalf("oracle rejected update %d: %v", i, err)
		}
	}
	return m.text()
}

func TestModelAgreesOnConcurrentInserts(t *testing.T) {
	a := NewDocWith(Options{ClientID: 5})
	b := NewDocWith(Options{ClientID: 9})
	a.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("body"), 0, "aa") })
	b.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("body"), 0, "bb") })
	ua := a.EncodeStateAsUpdate(StateVector{})
	ub := b.EncodeStateAsUpdate(StateVector{})
	if err := a.ApplyUpdate(ub, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.ApplyUpdate(ua, nil); err != nil {
		t.Fatal(err)
	}
	validate(t, a)
	validate(t, b)
	assertConverged(t, a, b, "aabb")
	// the oracle reaches the same text from either delivery order
	if got := modelText(t, ua, ub); got != "aabb" {
		t.Fatalf("the oracle reads %q after a then b", got)
	}
	if got := modelText(t, ub, ua); got != "aabb" {
		t.Fatalf("the oracle reads %q after b then a", got)
	}
}

func TestModelAgreesOnAConcurrentDelete(t *testing.T) {
	a := NewDocWith(Options{ClientID: 1})
	b := NewDocWith(Options{ClientID: 2})
	ta := a.Text("body")
	a.Transact(nil, func(tx *Tx) { tx.Insert(ta, 0, "abcdef") })
	base := a.EncodeStateAsUpdate(StateVector{})
	if err := b.ApplyUpdate(base, nil); err != nil {
		t.Fatal(err)
	}
	// one replica deletes the middle while the other types inside the range
	a.Transact(nil, func(tx *Tx) { tx.Delete(ta, 1, 4) })
	b.Transact(nil, func(tx *Tx) { tx.Insert(tx.Text("body"), 3, "ZZ") })
	ua := a.EncodeStateAsUpdate(StateVector{})
	ub := b.EncodeStateAsUpdate(StateVector{})
	if err := a.ApplyUpdate(ub, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.ApplyUpdate(ua, nil); err != nil {
		t.Fatal(err)
	}
	validate(t, a)
	validate(t, b)
	want := a.Text("body").String()
	if want != "aZZf" {
		t.Fatalf("the replicas read %q, want %q", want, "aZZf")
	}
	assertConverged(t, a, b, want)
	if got := modelText(t, ua, ub); got != want {
		t.Fatalf("the oracle reads %q after a then b, the replicas read %q", got, want)
	}
	if got := modelText(t, ub, ua); got != want {
		t.Fatalf("the oracle reads %q after b then a, the replicas read %q", got, want)
	}
}
