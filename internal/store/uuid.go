package store

import "github.com/josephburnett/gridwell/api/idshape"

// Identity minting moved to api/idshape (2026-08-15): id shape is
// CONTRACT, not storage — a third-party plugin minting a namespace and
// the host validating a hand-edited config must agree without importing
// each other. These names remain for the store's own call sites.

// NewUUID returns a fresh 128-bit provenance id (Tile.object_id).
func NewUUID() string { return idshape.NewUUID() }

func newUUID() string { return idshape.NewUUID() }

// NewShortID returns a fresh plugin/node/namespace identity (7-char
// lowercase base36, leading letter — owner decision 2026-07-25).
func NewShortID() string { return idshape.NewShortID() }
