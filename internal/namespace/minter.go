package namespace

// The one thing a namespace may offer beyond the wire method set. A plugin
// adapter accepts several id shapes for one thing, so without a mint a stored
// link could hold an id the listing never answers. The router asks before it
// stores; a namespace without Minter keeps the id it was given.

import "context"

// Minter turns a local id into the canonical local id a stored reference must
// hold. An id that is already canonical answers itself.
type Minter interface {
	MintRef(ctx context.Context, localID string) (string, error)
}

// MintRef mints through ns when it offers Minter, so no caller has to know
// which namespaces derive ids.
func MintRef(ctx context.Context, ns Namespace, localID string) (string, error) {
	m, ok := ns.(Minter)
	if !ok {
		return localID, nil
	}
	return m.MintRef(ctx, localID)
}
