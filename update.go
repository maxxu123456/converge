package converge

// Update is an encoded set of document changes. Applying one is commutative,
// associative and idempotent, and a nil or empty Update is a valid no-op.
type Update []byte
