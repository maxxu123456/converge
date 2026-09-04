// Package converge is a collaborative plain-text CRDT.
//
// A Doc holds named Text values that any number of replicas may edit at the
// same time. Replicas that have seen the same updates show the same text,
// whatever order those updates arrived in, and nobody has to ask a server which
// edit won. Concurrent inserts are ordered by YATA: an insert remembers the two
// runes it was typed between, and a tie between insertion points is broken on
// ClientID.
//
// Text is indexed in runes, not in bytes and not in UTF-16 code units.
// Text.UTF16Index and Text.RuneIndex convert at the boundary with an editor
// that counts the other way.
//
// # Concurrency
//
// One mutex guards a whole Doc, so every method is safe from any goroutine.
// Never copy a Doc.
//
// That mutex is held for the whole Transact callback, so inside one, read
// through the Tx. Tx.Len, Tx.String and Tx.Slice reuse the held lock, while
// Text.Len, Text.String and every Doc method would deadlock.
//
// Nothing here starts a goroutine, owns a timer, takes a context or needs
// closing. Awareness expiry is a Tick the application drives.
//
// # Observers
//
// Observe and OnUpdate callbacks are serialized, delivered in commit order, and
// run with the document lock released. A callback may run on a goroutine other
// than the one that made the edit, and may run after Transact has returned, so
// do not read Transact returning as the observer having run.
//
// Blocking inside a callback blocks every writer, so a provider feeding a slow
// peer must buffer or drop rather than wait. Editing from inside a callback is
// legal: the nested transaction commits, and its notification is delivered
// after the current callback returns.
//
// # Byte ownership
//
// Inbound, converge retains no argument. ApplyUpdate, ParseStateVector, the
// UnmarshalBinary methods and Awareness.Apply copy whatever they keep, so a
// network read buffer may be reused the moment the call returns.
//
// Outbound, the caller owns the result. Every Update, every MarshalBinary
// result, every Delta slice in an Event and every awareness blob is freshly
// allocated and never read again by converge.
//
// # Memory
//
// A delete leaves a tombstone behind. Nothing is ever reclaimed. Budget about
// 112 bytes per surviving run plus the UTF-8 bytes of its content, and note
// that runs merge, so a typing burst costs one run and not one per keystroke.
//
// Doc.Stats reports the numbers to alarm on. The mitigation is rotation: once
// TombstoneRunes passes both four times VisibleRunes and 100000, create a fresh
// Doc, insert String() into it, and move every peer to the new document id.
// Rotation throws away history, every outstanding update and every relative
// position, which is why converge will not do it behind your back.
package converge
