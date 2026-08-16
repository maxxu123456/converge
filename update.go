package converge

// Update is an encoded, self-describing set of document changes. Updates are
// commutative, associative and idempotent under ApplyUpdate. A nil or empty
// Update is a valid no-op.
type Update []byte
