// Package converge is a collaborative plain-text CRDT.
//
// A Doc holds named Text values that any number of replicas may edit at the
// same time. Replicas that have seen the same updates show the same text,
// whatever order those updates arrived in, and nobody has to ask a server which
// edit won. Concurrent inserts are ordered by YATA: an insert remembers the two
// runes it was typed bewteen, and a tie between insertion points is broken on
// ClientID.
//
// Text is indexed in runes, not in bytes and not in UTF-16 code units.
// Text.UTF16Index and Text.RuneIndex convert at the boundary with an editor
// that counts the other way.
//
// A delete leaves a tombstone behind. Nothing is ever reclaimed.
package converge
