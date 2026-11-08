package store

import "github.com/josephburnett/gridwell/api/idshape"

// Identity minting lives in api/idshape: id shape is contract, not storage, so
// a node and a client agree without importing each other.

// newUUID returns a fresh 128-bit id, the stored node uuid.
func newUUID() string { return idshape.NewUUID() }
