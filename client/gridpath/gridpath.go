// Package gridpath resolves a pane's descent path to its leaf grid. The
// decision lives here, not in the wasm shim, so it is unit-tested: a
// stale-prefix slip sends you to the wrong grid.
package gridpath

// ResolveLeafGrid walks a descent path from rootGridID, following each well
// tile's child grid, and returns the grid id at the leaf. It stops early —
// returning the LAST good grid id — when lookup reports the current grid
// isn't cached (gridCached=false) or a path id isn't a tile in it
// (tileFound=false): a stale path prefix resolves as deep as it can and
// never past a gap. Returns "" when rootGridID is "".
//
// lookup(gid, wellID) returns the well's child grid id plus whether the grid
// was cached and the tile found; the wasm caller does the cache read (and
// may kick a background fetch on a miss) inside it.
func ResolveLeafGrid(rootGridID string, path []string,
	lookup func(gid, wellID string) (childGridID string, gridCached, tileFound bool)) string {
	if rootGridID == "" {
		return ""
	}
	gid := rootGridID
	for _, wellID := range path {
		child, cached, found := lookup(gid, wellID)
		if !cached || !found {
			return gid
		}
		gid = child
	}
	return gid
}
