// Package syncproto frames converge messages for any duplex byte pipe.
//
// The protocol is symmetric, with no client and no server. On connect both
// peers send Step1 (a state vector), QueryAwareness, and their own awareness
// state. A Step1 is answered with Step2 (everything the sender holds that the
// state vector does not cover), and later local changes go out as Update.
// Initial sync is complete when the Step2 answering your own Step1 has been
// applied and Doc.Pending reports nothing outstanding.
//
// A frame is a uvarint payload length, a type byte, then the body, which makes
// it self-delimiting over a raw TCP stream. DecodeMessage and
// EncodeMessageUnframed drop the length prefix for transports that already
// deliver whole frames, a WebSocket binary message or a datagram, so nobody
// nests two length prefixes.
//
// Routing is a switch on Message.Type. There is deliberately no Apply helper:
// an error value must never be how a caller learns that a cursor moved.
package syncproto
