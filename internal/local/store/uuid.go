package store

import "github.com/josephburnett/gridwell/api/idshape"

// Identity minting lives in api/idshape: id shape is contract, not storage,
// because a third-party plugin minting a namespace and the host validating a
// hand-edited config must agree without importing each other. Only the
// store's own call site is here.

// newUUID returns a fresh 128-bit id (system.plugin_uuid).
func newUUID() string { return idshape.NewUUID() }
