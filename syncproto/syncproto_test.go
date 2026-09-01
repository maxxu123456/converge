package syncproto

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"math/rand"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/maxxu123456/converge"
)

const quietFor = 30 * time.Second

var errQuietTimeout = errors.New("network never went quiet")

// tracker counts messages in flight, so a test can wait for the network to
// settle without sleeping and without polling.
type tracker struct {
	mu   sync.Mutex
	cond *sync.Cond
	n    int
	err  error
}

func newTracker() *tracker {
	t := &tracker{}
	t.cond = sync.NewCond(&t.mu)
	return t
}

func (t *tracker) add(n int) {
	t.mu.Lock()
	t.n += n
	if t.n <= 0 {
		t.cond.Broadcast()
	}
	t.mu.Unlock()
}

func (t *tracker) fail(err error) {
	t.mu.Lock()
	if t.err == nil {
		t.err = err
	}
	t.cond.Broadcast()
	t.mu.Unlock()
}

// quiet blocks until nothing is in flight anywhere.
func (t *tracker) quiet(tb testing.TB) {
	tb.Helper()
	timer := time.AfterFunc(quietFor, func() { t.fail(errQuietTimeout) })
	defer timer.Stop()
	t.mu.Lock()
	for t.n > 0 && t.err == nil {
		t.cond.Wait()
	}
	err := t.err
	t.mu.Unlock()
	if err != nil {
		tb.Fatal(err)
	}
}

// outbox hands frames to the writer goroutine. Pushing never blocks, because
// an observer callback that blocks stalls every writer on the Doc.
type outbox struct {
	mu     sync.Mutex
	cond   *sync.Cond
	q      [][]byte
	closed bool
}

func newOutbox() *outbox {
	o := &outbox{}
	o.cond = sync.NewCond(&o.mu)
	return o
}

func (o *outbox) push(b []byte) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return false
	}
	o.q = append(o.q, b)
	o.cond.Signal()
	return true
}

func (o *outbox) take() [][]byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	for len(o.q) == 0 && !o.closed {
		o.cond.Wait()
	}
	q := o.q
	o.q = nil
	return q
}

func (o *outbox) close() {
	o.mu.Lock()
	o.closed = true
	o.cond.Broadcast()
	o.mu.Unlock()
}

// link is one end of a connection: a reader goroutine routing messages and a
// writer goroutine that re-chunks the byte stream and reorders whole frames.
type link struct {
	peer *peer
	conn net.Conn
	out  *outbox
	rng  *rand.Rand
	done chan struct{}
}

func (l *link) send(m Message) {
	l.peer.track.add(1)
	if !l.out.push(EncodeMessage(m)) {
		l.peer.track.add(-1)
	}
}

func (l *link) write() {
	for {
		frames := l.out.take()
		if len(frames) == 0 {
			return
		}
		// whole frames only: the byte stream itself must stay intact
		l.rng.Shuffle(len(frames), func(i, j int) { frames[i], frames[j] = frames[j], frames[i] })
		for i, f := range frames {
			if err := l.chunked(f); err != nil {
				l.out.close()
				l.peer.track.add(-(len(frames) - i))
				return
			}
		}
	}
}

func (l *link) chunked(f []byte) error {
	for len(f) > 0 {
		n := 1 + l.rng.Intn(len(f))
		if _, err := l.conn.Write(f[:n]); err != nil {
			return err
		}
		f = f[n:]
	}
	return nil
}

func (l *link) read() {
	defer close(l.done)
	br := bufio.NewReader(l.conn) // once per connection, never per call
	for {
		m, err := ReadMessage(br)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				l.peer.track.fail(err)
			}
			return
		}
		l.peer.route(l, m)
		l.peer.track.add(-1)
	}
}

// peer is one replica wired up the way the routing loop is meant to be wired.
type peer struct {
	doc   *converge.Doc
	track *tracker

	mu    sync.Mutex
	links []*link
}

func newPeer(t *testing.T, tr *tracker) *peer {
	p := &peer{doc: converge.NewDoc(), track: tr}
	cancel := p.doc.OnUpdate(func(u converge.Update, origin any) {
		p.broadcast(UpdateMessage(u), origin)
		if origin == nil {
			p.track.add(-1) // the edit that started this is now on the wire
		}
	})
	t.Cleanup(cancel)
	return p
}

func (p *peer) route(origin *link, m Message) {
	switch m.Type {
	case TypeStep1:
		sv, err := converge.ParseStateVector(m.Payload)
		if err != nil {
			p.track.fail(err)
			return
		}
		origin.send(Step2(p.doc.EncodeStateAsUpdate(sv)))
	case TypeStep2, TypeUpdate:
		if err := p.doc.ApplyUpdate(m.Payload, origin); err != nil {
			p.track.fail(err)
		}
	default:
		// a newer peer talking about something this build has no opinion on
	}
}

func (p *peer) broadcast(m Message, except any) {
	p.mu.Lock()
	links := slices.Clone(p.links)
	p.mu.Unlock()
	for _, l := range links {
		if any(l) != except {
			l.send(m)
		}
	}
}

// edit counts the local change until its update reaches the outboxes, so a
// quiet network cannot be observed before the edit has been dispatched.
func (p *peer) edit(name string, index int, s string) {
	txt := p.doc.Text(name)
	p.track.add(1)
	p.doc.Transact(nil, func(tx *converge.Tx) { tx.Insert(txt, index, s) })
}

func (p *peer) attach(t *testing.T, conn net.Conn, seed int64) *link {
	l := &link{peer: p, conn: conn, out: newOutbox(), rng: rand.New(rand.NewSource(seed)), done: make(chan struct{})}
	p.mu.Lock()
	p.links = append(p.links, l)
	p.mu.Unlock()
	go l.read()
	go l.write()
	t.Cleanup(func() { p.detach(l) })
	return l
}

func (p *peer) detach(l *link) {
	p.mu.Lock()
	p.links = slices.DeleteFunc(p.links, func(x *link) bool { return x == l })
	p.mu.Unlock()
	l.out.close()
	l.conn.Close()
	<-l.done
}

// connect runs the handshake both peers send on connect.
func connect(t *testing.T, a, b *peer, seed int64) {
	c1, c2 := net.Pipe()
	la := a.attach(t, c1, seed)
	lb := b.attach(t, c2, seed+1)
	la.send(Step1(a.doc.StateVector()))
	lb.send(Step1(b.doc.StateVector()))
}

func requireConverged(t *testing.T, name string, peers ...*peer) {
	t.Helper()
	want := peers[0].doc.Text(name).String()
	for i, p := range peers {
		if got := p.doc.Text(name).String(); got != want {
			t.Errorf("peer %d: text %q, want %q", i, got, want)
		}
		if n, missing := p.doc.Pending(); n != 0 {
			t.Errorf("peer %d: %d structs still pending, waiting for %v", i, n, missing)
		}
	}
}

func TestHandshakeConverges(t *testing.T) {
	cases := []struct {
		name  string
		setup func(a, b *peer)
	}{
		{"both empty", func(a, b *peer) {}},
		{"one empty", func(a, b *peer) { a.edit("body", 0, "hello") }},
		{"both divergent", func(a, b *peer) {
			a.edit("body", 0, "abc")
			b.edit("body", 0, "xyz")
		}},
		{"different roots", func(a, b *peer) {
			a.edit("body", 0, "left")
			b.edit("title", 0, "right")
		}},
		{"deletes on both sides", func(a, b *peer) {
			a.edit("body", 0, "hello world")
			txt := a.doc.Text("body")
			a.track.add(1)
			a.doc.Transact(nil, func(tx *converge.Tx) { tx.Delete(txt, 5, 6) })
			b.edit("body", 0, "xyz")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTracker()
			a, b := newPeer(t, tr), newPeer(t, tr)
			tc.setup(a, b)
			tr.quiet(t)
			connect(t, a, b, 7)
			tr.quiet(t)
			requireConverged(t, "body", a, b)
			requireConverged(t, "title", a, b)
		})
	}
}

func TestHandshakeDrainsABacklog(t *testing.T) {
	// a third replica's history, split so the second half is unapplicable alone
	maker := converge.NewDoc()
	var updates []converge.Update
	cancel := maker.OnUpdate(func(u converge.Update, _ any) { updates = append(updates, u) })
	txt := maker.Text("body")
	maker.Transact(nil, func(tx *converge.Tx) { tx.Insert(txt, 0, "hello") })
	maker.Transact(nil, func(tx *converge.Tx) { tx.Insert(txt, 5, " world") })
	cancel()
	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2", len(updates))
	}

	tr := newTracker()
	a, b := newPeer(t, tr), newPeer(t, tr)
	for _, u := range updates {
		if err := a.doc.ApplyUpdate(u, "seed"); err != nil {
			t.Fatalf("seeding a: %v", err)
		}
	}
	if err := b.doc.ApplyUpdate(updates[1], "seed"); err != nil {
		t.Fatalf("seeding b: %v", err)
	}
	if n, _ := b.doc.Pending(); n == 0 {
		t.Fatal("b should be holding a causally blocked struct")
	}

	connect(t, a, b, 11)
	tr.quiet(t)
	requireConverged(t, "body", a, b)
	if got := a.doc.Text("body").String(); got != "hello world" {
		t.Errorf("text %q, want %q", got, "hello world")
	}
}

func TestThreePeersRelay(t *testing.T) {
	tr := newTracker()
	a, b, c := newPeer(t, tr), newPeer(t, tr), newPeer(t, tr)
	a.edit("body", 0, "aaa")
	b.edit("body", 0, "bbb")
	c.edit("body", 0, "ccc")
	tr.quiet(t)

	// a ring, so every update reaches each peer twice and the duplicate stops
	connect(t, a, b, 21)
	connect(t, b, c, 31)
	connect(t, c, a, 41)
	tr.quiet(t)
	requireConverged(t, "body", a, b, c)

	b.edit("body", 1, "-mid-")
	tr.quiet(t)
	requireConverged(t, "body", a, b, c)
	if got := a.doc.Text("body").String(); len(got) != 14 {
		t.Errorf("text %q, want 14 runes", got)
	}
}

func TestPeerJoinsMidSession(t *testing.T) {
	tr := newTracker()
	a, b := newPeer(t, tr), newPeer(t, tr)
	connect(t, a, b, 51)
	a.edit("body", 0, "hello")
	tr.quiet(t)
	b.edit("body", 5, " world")
	tr.quiet(t)

	c := newPeer(t, tr)
	connect(t, c, a, 61)
	tr.quiet(t)
	requireConverged(t, "body", a, b, c)
	if got := c.doc.Text("body").String(); got != "hello world" {
		t.Errorf("joiner has %q, want %q", got, "hello world")
	}

	c.edit("body", 11, "!")
	tr.quiet(t)
	requireConverged(t, "body", a, b, c)
}

func TestReconnectAfterPartition(t *testing.T) {
	tr := newTracker()
	a, b := newPeer(t, tr), newPeer(t, tr)
	connect(t, a, b, 71)
	a.edit("body", 0, "shared")
	tr.quiet(t)
	requireConverged(t, "body", a, b)

	a.mu.Lock()
	la := a.links[0]
	a.mu.Unlock()
	b.mu.Lock()
	lb := b.links[0]
	b.mu.Unlock()
	a.detach(la)
	b.detach(lb)

	a.edit("body", 6, " from a")
	b.edit("body", 0, "b says: ")
	tr.quiet(t)
	if a.doc.Text("body").String() == b.doc.Text("body").String() {
		t.Fatal("the partition did not actually separate the peers")
	}

	connect(t, a, b, 81)
	tr.quiet(t)
	requireConverged(t, "body", a, b)
}

func TestFrameRoundTrip(t *testing.T) {
	msgs := []Message{
		Step1(converge.NewDoc().StateVector()),
		Step2(converge.Update("\xcf\x01\x01\x00\x00")),
		UpdateMessage(converge.Update("\xcf\x01\x01\x00\x00")),
		QueryAwareness(),
		AwarenessMessage([]byte("opaque")),
	}
	var stream []byte
	for _, m := range msgs {
		stream = append(stream, EncodeMessage(m)...)
	}
	br := bufio.NewReader(bytes.NewReader(stream))
	for i, want := range msgs {
		got, err := ReadMessage(br)
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if got.Type != want.Type || !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("message %d: got %v %x, want %v %x", i, got.Type, got.Payload, want.Type, want.Payload)
		}
		unframed, err := DecodeMessage(EncodeMessageUnframed(want))
		if err != nil {
			t.Fatalf("unframed message %d: %v", i, err)
		}
		if unframed.Type != want.Type || !bytes.Equal(unframed.Payload, want.Payload) {
			t.Errorf("unframed message %d: got %v %x", i, unframed.Type, unframed.Payload)
		}
	}
	if _, err := ReadMessage(br); !errors.Is(err, io.EOF) {
		t.Errorf("after the last frame: got %v, want EOF", err)
	}
}

func TestStep1WireBytes(t *testing.T) {
	blob := []byte{0xcf, 0x02, 0x01, 0x01, 0x2a, 0x02}
	var sv converge.StateVector
	if err := sv.UnmarshalBinary(blob); err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0x07, 0x00}, blob...)
	if got := EncodeMessage(Step1(sv)); !bytes.Equal(got, want) {
		t.Errorf("got % x, want % x", got, want)
	}
}

func TestTypeString(t *testing.T) {
	want := []string{"Step1", "Step2", "Update", "QueryAwareness", "Awareness", "Type(5)"}
	for i, s := range want {
		if got := Type(i).String(); got != s {
			t.Errorf("Type(%d): got %q, want %q", i, got, s)
		}
	}
}

func TestMalformedFrames(t *testing.T) {
	cases := []struct {
		name  string
		frame []byte
		want  error
	}{
		{"empty payload", []byte{0x00}, ErrMalformedMessage},
		{"unknown type", []byte{0x01, 0x05}, ErrMalformedMessage},
		{"truncated payload", []byte{0x04, 0x02, 0xcf}, io.ErrUnexpectedEOF},
		{"truncated prefix", []byte{0x80}, io.ErrUnexpectedEOF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br := bufio.NewReader(bytes.NewReader(tc.frame))
			if _, err := ReadMessage(br); !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
	for _, frame := range [][]byte{nil, {0x05}, {0x05, 0xcf}} {
		if _, err := DecodeMessage(frame); !errors.Is(err, ErrMalformedMessage) {
			t.Errorf("DecodeMessage(% x): got %v, want %v", frame, err, ErrMalformedMessage)
		}
	}
}

func TestReadMessageLimitRefusesEarly(t *testing.T) {
	payload := []byte{byte(TypeUpdate), 0xcf, 0x01}
	frame := append([]byte{0xc0, 0x84, 0x3d}, payload...) // declares 1000000 bytes
	br := bufio.NewReader(bytes.NewReader(frame))
	if _, err := ReadMessageLimit(br, 1024); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("got %v, want %v", err, ErrMessageTooLarge)
	}
	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rest, payload) {
		t.Errorf("payload was consumed: % x left, want % x", rest, payload)
	}
}

// replayReader hands out one prefix over and over, so an allocation count
// measures the framing and not the reader.
type replayReader struct {
	b []byte
	i int
}

func (r *replayReader) ReadByte() (byte, error) {
	b := r.b[r.i%len(r.b)]
	r.i++
	return b, nil
}

func (r *replayReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i], _ = r.ReadByte()
	}
	return len(p), nil
}

func TestReadMessageLimitDoesNotAllocate(t *testing.T) {
	r := &replayReader{b: []byte{0xff, 0xff, 0xff, 0xff, 0x0f}} // 4 GiB and change
	got := testing.AllocsPerRun(100, func() {
		r.i = 0
		if _, err := ReadMessageLimit(r, 1<<20); !errors.Is(err, ErrMessageTooLarge) {
			t.Fatalf("got %v, want %v", err, ErrMessageTooLarge)
		}
	})
	if got != 0 {
		t.Errorf("got %v allocations per refused frame, want 0", got)
	}
}

func TestZeroLimitFallsBackToTheDefault(t *testing.T) {
	frame := EncodeMessage(AwarenessMessage([]byte("hi")))
	br := bufio.NewReader(bytes.NewReader(frame))
	m, err := ReadMessageLimit(br, 0)
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != TypeAwareness || string(m.Payload) != "hi" {
		t.Errorf("got %v %q", m.Type, m.Payload)
	}
}
