package awareness

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/maxxu123456/converge"
)

func newAwareness(t *testing.T, id converge.ClientID) *Awareness {
	t.Helper()
	return New(converge.NewDocWith(converge.Options{ClientID: id}))
}

func apply(t *testing.T, a *Awareness, update []byte) ([]converge.ClientID, []byte) {
	t.Helper()
	changed, reply, err := a.Apply(update)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return changed, reply
}

func TestLocalIdentityComesFromTheDoc(t *testing.T) {
	d := converge.NewDoc()
	a := New(d)
	if a.ClientID() != d.ClientID() {
		t.Errorf("ClientID %v, want %v", a.ClientID(), d.ClientID())
	}
	if got := a.LocalState(); got != nil {
		t.Errorf("LocalState before announcing: got %q, want nil", got)
	}
	if got := a.States(); len(got) != 0 {
		t.Errorf("States before announcing: got %v", got)
	}
}

func TestSetLocalStateWireBytes(t *testing.T) {
	a := newAwareness(t, 42)
	got := a.SetLocalState([]byte("hello"))
	want := []byte{0xcf, 0x03, 0x01, 0x01, 0x2a, 0x01, 0x06, 'h', 'e', 'l', 'l', 'o'}
	if !bytes.Equal(got, want) {
		t.Errorf("got % x, want % x", got, want)
	}
}

func TestOpaqueStatesRoundTrip(t *testing.T) {
	states := [][]byte{
		[]byte("cursor"),
		{},
		{0x00, 0xff, 0x7f, 0x80},
		bytes.Repeat([]byte{0xab}, 300),
	}
	for _, want := range states {
		a, b := newAwareness(t, 1), newAwareness(t, 2)
		apply(t, b, a.SetLocalState(want))
		got := b.States()[a.ClientID()]
		if !bytes.Equal(got, want) || (got == nil) != (want == nil) {
			t.Errorf("state %q came back as %q", want, got)
		}
	}
}

func TestNilStateIsRemovalNotEmptyState(t *testing.T) {
	a, b := newAwareness(t, 1), newAwareness(t, 2)
	apply(t, b, a.SetLocalState([]byte{}))
	if _, ok := b.States()[1]; !ok {
		t.Fatal("an empty state should still be present")
	}
	apply(t, b, a.SetLocalState(nil))
	if _, ok := b.States()[1]; ok {
		t.Error("a nil state should read as gone")
	}
	if got := a.LocalState(); got != nil {
		t.Errorf("LocalState after a nil: got %q", got)
	}
}

func TestStaleUpdateIgnored(t *testing.T) {
	a, b := newAwareness(t, 1), newAwareness(t, 2)
	first := a.SetLocalState([]byte("one"))
	second := a.SetLocalState([]byte("two"))
	apply(t, b, second)
	changed, _ := apply(t, b, first)
	if len(changed) != 0 {
		t.Errorf("a stale update changed %v", changed)
	}
	if got := b.States()[1]; string(got) != "two" {
		t.Errorf("state %q, want %q", got, "two")
	}
	// the same update twice is also stale the second time
	if changed, _ := apply(t, b, second); len(changed) != 0 {
		t.Errorf("a repeated update changed %v", changed)
	}
}

func TestClockMonotonicUnderReordering(t *testing.T) {
	a := newAwareness(t, 1)
	updates := [][]byte{
		a.SetLocalState([]byte("one")),
		a.SetLocalState([]byte("two")),
		a.SetLocalState([]byte("three")),
	}
	orders := [][]int{{0, 1, 2}, {2, 1, 0}, {1, 2, 0}, {2, 0, 1}, {0, 2, 1}}
	for _, order := range orders {
		b := newAwareness(t, 2)
		for _, i := range order {
			apply(t, b, updates[i])
		}
		if got := b.States()[1]; string(got) != "three" {
			t.Errorf("order %v: state %q, want %q", order, got, "three")
		}
	}
}

func TestRemovalResistsResurrection(t *testing.T) {
	a, b := newAwareness(t, 1), newAwareness(t, 2)
	announce := a.SetLocalState([]byte("here"))
	apply(t, b, announce)
	update := b.Remove(1)
	if _, ok := b.States()[1]; ok {
		t.Fatal("the removed client is still live on the remover")
	}

	c := newAwareness(t, 3)
	apply(t, c, announce)
	changed, _ := apply(t, c, update)
	if !slices.Equal(changed, []converge.ClientID{1}) {
		t.Errorf("the removal changed %v, want [1]", changed)
	}
	// the announcement is still in flight somewhere and must not bring it back
	for _, target := range []*Awareness{b, c} {
		if changed, _ := apply(t, target, announce); len(changed) != 0 {
			t.Errorf("a replayed announcement changed %v", changed)
		}
		if _, ok := target.States()[1]; ok {
			t.Error("a replayed announcement resurrected a departed peer")
		}
	}
}

func TestRemoveBumpsTheClockAndBroadcasts(t *testing.T) {
	a, b := newAwareness(t, 1), newAwareness(t, 2)
	announce := a.SetLocalState([]byte("here"))
	apply(t, b, announce)
	// a third replica hears the removal first and still ignores the older
	// announcement, because Remove wrote one clock above it
	c := newAwareness(t, 3)
	apply(t, c, b.Remove(1))
	if _, ok := c.States()[1]; ok {
		t.Error("client 1 is live on c")
	}
	if changed, _ := apply(t, c, announce); len(changed) != 0 {
		t.Errorf("the older announcement changed %v", changed)
	}
}

func TestReannounceWhenAPeerEvictsALiveLocalState(t *testing.T) {
	a, b := newAwareness(t, 1), newAwareness(t, 2)
	apply(t, b, a.SetLocalState([]byte("still here")))

	changed, reply := apply(t, a, b.Remove(1))
	if reply == nil {
		t.Fatal("no re-announcement for a live local state")
	}
	if !slices.Equal(changed, []converge.ClientID{1}) {
		t.Errorf("changed %v, want [1]", changed)
	}
	if got := a.LocalState(); string(got) != "still here" {
		t.Errorf("local state %q, want %q", got, "still here")
	}

	apply(t, b, reply)
	if got := b.States()[1]; string(got) != "still here" {
		t.Errorf("after the reply b has %q, want %q", got, "still here")
	}
}

func TestNoReannounceForAGoneLocalState(t *testing.T) {
	a, b := newAwareness(t, 1), newAwareness(t, 2)
	apply(t, b, a.SetLocalState([]byte("here")))
	a.SetLocalState(nil) // a clean disconnect, so b's removal is agreement
	_, reply := apply(t, a, b.Remove(1))
	if reply != nil {
		t.Errorf("re-announced a state that is gone: % x", reply)
	}
	if got := a.LocalState(); got != nil {
		t.Errorf("local state came back as %q", got)
	}
}

func TestEncodeSelectsClients(t *testing.T) {
	a := newAwareness(t, 1)
	b := newAwareness(t, 2)
	a.SetLocalState([]byte("mine"))
	apply(t, a, b.SetLocalState([]byte("theirs")))

	all, err := decode(a.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].client != 1 || all[1].client != 2 {
		t.Errorf("Encode() gave %+v, want both clients ascending", all)
	}
	one, err := decode(a.Encode(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].client != 2 {
		t.Errorf("Encode(2) gave %+v", one)
	}
	none, err := decode(a.Encode(9))
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("Encode of an unknown client gave %+v", none)
	}
	// duplicate arguments must not encode a client twice
	dup, err := decode(a.Encode(1, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(dup) != 2 {
		t.Errorf("Encode(1, 1, 2) gave %+v", dup)
	}
}

func TestApplyRejectsMalformed(t *testing.T) {
	good := []byte{0xcf, 0x03, 0x01, 0x01, 0x2a, 0x01, 0x06, 'h', 'e', 'l', 'l', 'o'}
	cases := map[string][]byte{
		"empty":             {},
		"short header":      {0xcf, 0x03},
		"bad magic":         {0xce, 0x03, 0x01, 0x00},
		"bad kind":          {0xcf, 0x01, 0x01, 0x00},
		"bad version":       {0xcf, 0x03, 0x02, 0x00},
		"client zero":       {0xcf, 0x03, 0x01, 0x01, 0x00, 0x01, 0x00},
		"non-ascending":     {0xcf, 0x03, 0x01, 0x02, 0x02, 0x01, 0x00, 0x01, 0x01, 0x00},
		"repeated client":   {0xcf, 0x03, 0x01, 0x02, 0x02, 0x01, 0x00, 0x02, 0x01, 0x00},
		"count overruns":    {0xcf, 0x03, 0x01, 0x7f},
		"non-minimal count": {0xcf, 0x03, 0x01, 0x80, 0x00},
		"state overruns":    {0xcf, 0x03, 0x01, 0x01, 0x2a, 0x01, 0x09, 'h', 'i'},
		"trailing bytes":    append(slices.Clone(good), 0x00),
	}
	for name, blob := range cases {
		t.Run(name, func(t *testing.T) {
			a := newAwareness(t, 7)
			changed, reply, err := a.Apply(blob)
			if !errors.Is(err, ErrMalformedAwarenessUpdate) {
				t.Fatalf("got %v, want %v", err, ErrMalformedAwarenessUpdate)
			}
			if changed != nil || reply != nil {
				t.Errorf("a rejected update reported %v %x", changed, reply)
			}
			if len(a.States()) != 0 {
				t.Error("a rejected update was partly applied")
			}
		})
	}
	for _, blob := range [][]byte{good, {0xcf, 0x03, 0x01, 0x00}} {
		a := newAwareness(t, 7)
		if _, _, err := a.Apply(blob); err != nil {
			t.Errorf("Apply(% x): %v", blob, err)
		}
	}
}

func TestReturnedSlicesAreOwned(t *testing.T) {
	a, b := newAwareness(t, 1), newAwareness(t, 2)
	state := []byte("mine")
	update := a.SetLocalState(state)
	state[0] = 'x' // the argument was copied
	if got := a.LocalState(); string(got) != "mine" {
		t.Errorf("SetLocalState kept the caller's slice: %q", got)
	}

	local := a.LocalState()
	local[0] = 'y'
	if got := a.LocalState(); string(got) != "mine" {
		t.Errorf("LocalState aliases the entry: %q", got)
	}

	apply(t, b, update)
	states := b.States()
	states[1][0] = 'z'
	delete(states, 1)
	if got := b.States()[1]; string(got) != "mine" {
		t.Errorf("States aliases the entries: %q", got)
	}

	encoded := a.Encode()
	clone := slices.Clone(encoded)
	encoded[3] = 0xff
	if got := a.Encode(); !bytes.Equal(got, clone) {
		t.Errorf("Encode reuses a buffer: % x, want % x", got, clone)
	}
}

// The Tick tests offset from a base time, so expiry is exact and nothing sleeps.
func TestTickEvictsAfterTheTimeout(t *testing.T) {
	base := time.Now()
	a := newAwareness(t, 1)
	apply(t, a, newAwareness(t, 5).SetLocalState([]byte("five")))
	apply(t, a, newAwareness(t, 3).SetLocalState([]byte("three")))
	a.SetLocalState([]byte("mine"))

	if _, removed := a.Tick(base.Add(29*time.Second), DefaultTimeout); len(removed) != 0 {
		t.Fatalf("evicted %v inside the timeout", removed)
	}
	_, removed := a.Tick(base.Add(31*time.Second), DefaultTimeout)
	if !slices.Equal(removed, []converge.ClientID{3, 5}) {
		t.Errorf("removed %v, want [3 5] ascending", removed)
	}
	if got := a.States(); len(got) != 1 {
		t.Errorf("States after eviction: %v", got)
	}
}

func TestTickNeverEvictsTheLocalClient(t *testing.T) {
	a := newAwareness(t, 1)
	a.SetLocalState([]byte("mine"))
	update, removed := a.Tick(time.Now().Add(time.Hour), time.Second)
	if len(removed) != 0 {
		t.Errorf("removed %v, want nothing", removed)
	}
	if got := a.LocalState(); string(got) != "mine" {
		t.Errorf("local state %q, want %q", got, "mine")
	}
	es, err := decode(update)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 || es[0].client != 1 || es[0].clock != 2 || string(es[0].state) != "mine" {
		t.Errorf("heartbeat %+v, want client 1 at clock 2 with the same state", es)
	}
}

func TestTickHeartbeatIsAcceptedByAPeer(t *testing.T) {
	a, b := newAwareness(t, 1), newAwareness(t, 2)
	apply(t, b, a.SetLocalState([]byte("mine")))
	now := time.Now()
	for i := 0; i < 3; i++ {
		update, _ := a.Tick(now, DefaultTimeout)
		changed, _ := apply(t, b, update)
		if !slices.Equal(changed, []converge.ClientID{1}) {
			t.Fatalf("tick %d changed %v, want [1]", i, changed)
		}
		if got := b.States()[1]; string(got) != "mine" {
			t.Errorf("tick %d: b has %q", i, got)
		}
	}
}

func TestEvictedPeerReturnsOnItsNextHeartbeat(t *testing.T) {
	base := time.Now()
	a, b := newAwareness(t, 1), newAwareness(t, 2)
	apply(t, a, b.SetLocalState([]byte("theirs")))
	if _, removed := a.Tick(base.Add(time.Minute), DefaultTimeout); !slices.Equal(removed, []converge.ClientID{2}) {
		t.Fatalf("removed %v, want [2]", removed)
	}
	// eviction forgets the clock too, so the next heartbeat is a fresh peer
	update, _ := b.Tick(base, DefaultTimeout)
	changed, _ := apply(t, a, update)
	if !slices.Equal(changed, []converge.ClientID{2}) {
		t.Errorf("changed %v, want [2]", changed)
	}
	if got := a.States()[2]; string(got) != "theirs" {
		t.Errorf("a has %q, want %q", got, "theirs")
	}
}

func TestTickOnAnUnannouncedLocalState(t *testing.T) {
	a := newAwareness(t, 1)
	update, removed := a.Tick(time.Now(), DefaultTimeout)
	if len(removed) != 0 {
		t.Errorf("removed %v", removed)
	}
	es, err := decode(update)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 || es[0].state != nil {
		t.Errorf("heartbeat %+v, want a removal for the silent local client", es)
	}
	// and announcing later still outruns that heartbeat
	if got := a.SetLocalState([]byte("mine")); !bytes.Equal(got[:6], []byte{0xcf, 0x03, 0x01, 0x01, 0x01, 0x02}) {
		t.Errorf("first announcement % x, want clock 2", got)
	}
}
