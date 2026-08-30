// Package gridpath holds the pure navigation decision over a pane's
// descent path: resolving a path to its leaf grid. It was inline in
// build-tagged wasm (never run by `go test`) though it decides where you
// land — a stale-prefix slip sends you to the wrong grid.
//
// The ascent classifier that used to live here (AscentWalk /
// ClassifyAscent) is gone with S8: an ascent pops exactly ONE frame, and
// that frame REMEMBERS the doorway it came in by, so there is no leaf-ward
// re-walk to classify.
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
