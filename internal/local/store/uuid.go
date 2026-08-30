package store

import "github.com/josephburnett/gridwell/api/idshape"

// Identity minting moved to api/idshape (2026-08-15): id shape is
// CONTRACT, not storage — a third-party plugin minting a namespace and
// the host validating a hand-edited config must agree without importing
// each other. Only the store's own call site remains.

// newUUID returns a fresh 128-bit id (system.plugin_uuid).
func newUUID() string { return idshape.NewUUID() }
