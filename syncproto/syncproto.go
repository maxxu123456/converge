package syncproto

import (
	"encoding/binary"
	"errors"
	"io"
	"strconv"

	"github.com/maxxu123456/converge"
)

// Type is a sync message kind.
type Type uint8

const (
	TypeStep1          Type = 0 // body: an encoded StateVector
	TypeStep2          Type = 1 // body: an encoded Update, the reply to a Step1
	TypeUpdate         Type = 2 // body: an encoded Update, broadcast
	TypeQueryAwareness Type = 3 // body: empty
	TypeAwareness      Type = 4 // body: an opaque awareness update
)

// String names the type, for logs.
func (t Type) String() string {
	switch t {
	case TypeStep1:
		return "Step1"
	case TypeStep2:
		return "Step2"
	case TypeUpdate:
		return "Update"
	case TypeQueryAwareness:
		return "QueryAwareness"
	case TypeAwareness:
		return "Awareness"
	}
	return "Type(" + strconv.Itoa(int(t)) + ")"
}

// Message is one framed protocol message. Payload is owned by the receiver of
// the Message value.
type Message struct {
	Type    Type
	Payload []byte
}

// ByteReader is what a framed read needs. Wrap a connection in ONE
// *bufio.Reader for the life of that connection, never one per call, or you
// lose the bytes buffered past the end of a message.
type ByteReader interface {
	io.Reader
	io.ByteReader
}

// ErrMalformedMessage is returned when a frame violates the framing rules.
var ErrMalformedMessage = errors.New("syncproto: malformed message")

// ReadMessage reads one length-prefixed message.
func ReadMessage(r ByteReader) (Message, error) {
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return Message{}, err
	}
	// a frame carries at least the type byte
	if n == 0 {
		return Message{}, ErrMalformedMessage
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Message{}, err
	}
	return split(buf)
}

// WriteMessage writes one length-prefixed message.
func WriteMessage(w io.Writer, m Message) error {
	// one Write, so a datagram writer sees the whole frame
	_, err := w.Write(EncodeMessage(m))
	return err
}

// EncodeMessage returns the length-prefixed encoding of m.
func EncodeMessage(m Message) []byte {
	b := make([]byte, 0, binary.MaxVarintLen64+1+len(m.Payload))
	b = binary.AppendUvarint(b, uint64(len(m.Payload))+1)
	b = append(b, byte(m.Type))
	return append(b, m.Payload...)
}

// EncodeMessageUnframed returns m without the length prefix, for transports
// that frame for you.
func EncodeMessageUnframed(m Message) []byte {
	b := make([]byte, 0, 1+len(m.Payload))
	b = append(b, byte(m.Type))
	return append(b, m.Payload...)
}

// DecodeMessage decodes a message whose framing the transport already provides
// (a WebSocket binary frame, a datagram). It expects NO length prefix, and the
// returned Payload aliases frame.
func DecodeMessage(frame []byte) (Message, error) { return split(frame) }

// split separates the type byte from the body of an unprefixed frame.
func split(p []byte) (Message, error) {
	if len(p) == 0 || p[0] > byte(TypeAwareness) {
		return Message{}, ErrMalformedMessage
	}
	return Message{Type: Type(p[0]), Payload: p[1:]}, nil
}

// Step1 offers sv, asking the peer for everything it holds that sv does not.
func Step1(sv converge.StateVector) Message {
	b, _ := sv.MarshalBinary() // infallible
	return Message{Type: TypeStep1, Payload: b}
}

// Step2 answers a Step1.
func Step2(u converge.Update) Message { return Message{Type: TypeStep2, Payload: u} }

// UpdateMessage broadcasts an update nobody asked for.
func UpdateMessage(u converge.Update) Message { return Message{Type: TypeUpdate, Payload: u} }

// QueryAwareness asks a peer for every presence state it knows.
func QueryAwareness() Message { return Message{Type: TypeQueryAwareness} }

// AwarenessMessage carries an opaque awareness update.
func AwarenessMessage(b []byte) Message { return Message{Type: TypeAwareness, Payload: b} }
