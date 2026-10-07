// Package contentrow owns which row a tile's content facts are read from: a
// content row's own, a leaf link's target's. A link row carries none of them
// (the store's link CHECK, pluginhost.buildTiles), so an address, serves_page,
// read_only, text_presentation or preview read off the link is an empty
// value, never an answer. It is js-free; the shim supplies the cache lookup
// and reads the target a Pending verdict names.
package contentrow

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// State says whether the row Of answers can be read.
type State int

const (
	// Ready: the row is the one whose facts the tile presents.
	Ready State = iota
	// Pending: the row has not landed, or a link's target is in no cached
	// grid. The caller reads what Ask names and decides again when it lands.
	Pending
	// Dead: the link's path ends in nothing, so there are no facts to read.
	Dead
)

// Of is the row whose content facts t presents. cached looks a row up by id
// without fetching; dead is deadref's verdict on t.
func Of(t *gridwellv1.Tile, cached func(id string) *gridwellv1.Tile, dead bool) (*gridwellv1.Tile, State) {
	switch {
	case t == nil:
		return nil, Pending
	case !rpc.LeafLink(t):
		return t, Ready
	case dead:
		return nil, Dead
	}
	if row := cached(t.LinkTargetId); row != nil {
		return row, Ready
	}
	return nil, Pending
}

// Ask is the id to read for verdict s on t, "" when there is none: a row that
// has not landed is its own descent's read.
func Ask(t *gridwellv1.Tile, s State) string {
	if s != Pending || t == nil {
		return ""
	}
	return t.LinkTargetId
}
