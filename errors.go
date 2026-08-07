package converge

import (
	"errors"
	"fmt"
)

// ErrMalformedUpdate is returned (usually wrapped in a *DecodeError) when bytes
// offered as an Update violate the wire format or its strictness rules.
var ErrMalformedUpdate = errors.New("converge: malformed update")

// ErrUnsupportedVersion is returned when a blob carries a format version this
// build does not implement. No further bytes are read.
var ErrUnsupportedVersion = errors.New("converge: unsupported format version")

// ErrMalformedStateVector is returned when bytes offered as a state vector
// violate the wire format.
var ErrMalformedStateVector = errors.New("converge: malformed state vector")

// ErrMalformedPosition is returned when bytes offered as a Position violate the
// wire format.
var ErrMalformedPosition = errors.New("converge: malformed position")

// ErrPendingOverflow is returned by ApplyUpdate when accepting the causally
// blocked remainder of an update would exceed Options.MaxPendingStructs. The
// ready part of the update HAS been applied, the blocked remainder was
// discarded. Recover by sending a fresh SyncStep1 on every link.
var ErrPendingOverflow = errors.New("converge: causally-blocked buffer full")

// DecodeError locates a decode failure. Err is one of the sentinels above.
type DecodeError struct {
	Offset int    // byte offset into the blob where the failure was detected
	Field  string // e.g. "struct.contentLen", "deleteSet.gap"
	Err    error
}

func (e *DecodeError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s at byte %d", e.Err, e.Offset)
	}
	return fmt.Sprintf("%s at byte %d in %s", e.Err, e.Offset, e.Field)
}

func (e *DecodeError) Unwrap() error { return e.Err }

// RangeError is the value panicked when an index or length argument is out of
// range. Indices are programmer input, so converge panics exactly as slice
// indexing does. RangeError implements error so a recovering caller can
// type-assert it.
type RangeError struct {
	Text   string // name of the Text
	Index  int
	Length int
	Len    int // the Text's visible length at the time of the call
}

func (e *RangeError) Error() string {
	return fmt.Sprintf("converge: text %q: index %d length %d out of range for length %d",
		e.Text, e.Index, e.Length, e.Len)
}

// UsageError is the value panicked for an API contract violation that is always
// a programming error: a *Tx used after its callback returned, a *Text passed to
// a *Tx of a different Doc, an empty or invalid Text name, or invalid UTF-8
// offered to Insert.
type UsageError struct {
	Msg string
}

func (e *UsageError) Error() string { return "converge: " + e.Msg }
