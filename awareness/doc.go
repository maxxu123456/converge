// Package awareness tracks who else is in a document: cursors, names, colours.
//
// States are opaque bytes, so converge has no opinion about your schema and
// never imports encoding/json. Two marshalled positions and a colour fit in
// about forty bytes where the JSON of the same thing is two hundred.
//
// Nothing here starts a goroutine or owns a timer. Drive Tick from your own
// ticker, every RecommendedTickInterval against DefaultTimeout. Its heartbeat
// is not decorative: without it an idle reader's cursor vanishes from every
// screen after the timeout and only comes back when they type.
//
// Awareness is not part of the document. It is not persisted and does not
// participate in the CRDT.
package awareness
