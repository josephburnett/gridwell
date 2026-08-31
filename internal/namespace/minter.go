package namespace

// Minter is the one thing a namespace may offer beyond the wire method set:
// turning a DERIVED id into the row it stands for.
//
// A namespace whose ids are all rows — home — needs nothing here. A namespace
// that answers untouched things by what they are, which is
// pluginhost.Adapter, needs a way to say "a reference is about to be stored at
// you, so mint it": a reference at rest must name a row, or a listing that
// reflows would leave it pointing somewhere else. The router asks before it
// stores, and a namespace that does not implement Minter simply keeps the id
// it was given, which is what home does and what a mount of another node does
// — the far node mints its own rows, and there is no wire verb to ask it to.
//
// It is deliberately NOT part of Namespace: that interface is the gridwell.v1
// service's method set in Go, and this is not a wire verb.

import "context"

// Minter turns a local id into the canonical local id a stored reference must
// hold, minting the row if there is not one yet. A row id is already
// canonical and answers itself.
type Minter interface {
	MintRef(ctx context.Context, localID string) (string, error)
}

// MintRef asks ns to canonicalize localID if it can, and otherwise leaves it
// alone. It is the one call site's worth of type assertion, written once so
// the router never has to know which namespaces derive ids.
func MintRef(ctx context.Context, ns Namespace, localID string) (string, error) {
	m, ok := ns.(Minter)
	if !ok {
		return localID, nil
	}
	return m.MintRef(ctx, localID)
}
