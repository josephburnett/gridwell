// Package urlwalk holds the pure boot-time descent walk: resolving a
// URL's tile-id list against the user's grids into a descent path plus an
// optional trailing file-tile. Extracted from the wasm client (where it
// was entangled with cache fetches) so the state-machine — skip missing
// ids, switch grids at well boundaries, stop at a content leaf — gets real
// `go test` coverage. A misstep here lands the user in the wrong grid.
package urlwalk

import "strconv"

// Tile is the minimum a walk step needs to know about a tile: whether it
// descends into a child grid (a well), whether it is a content leaf
// (text/url), and — for a well — which grid it points at. The caller
// classifies kinds (via rpc.IsWellKind / rpc.IsContentDescentKind) when
// building these, keeping this package free of wire types.
type Tile struct {
	ChildGridID string
	IsWell      bool
	IsContent   bool
}

// GridLookup returns the tiles of grid gid, fetching and caching it if
// necessary. It returns false when the grid can't be loaded — the walk
// then stops where it is (a failed fetch never invents a path). It is
// called for the root grid and for each well child grid descended into.
type GridLookup func(gid int64) (tiles map[int64]Tile, ok bool)

// Walk resolves tileIDs against the grids reachable from rootGridID and
// returns the descent path (well ids, in order) plus the trailing
// file-tile id (0 if the leaf is a grid, not a content tile).
//
// Rules (loose on input, by design — a bookmarked URL must degrade
// gracefully as the canvas changes underneath it):
//   - An id missing from the current grid is skipped; the walk stays in
//     the same grid and tries the next id.
//   - A well id is appended to the path and the walk descends into its
//     child grid.
//   - A content tile is accepted only as the LAST id; a content tile
//     mid-path is nonsense and skipped.
//   - A grid that fails to load ends the walk with what's resolved so far.
func Walk(rootGridID int64, tileIDs []int64, lookup GridLookup) (path []int64, fileTileID int64) {
	gid := rootGridID
	path = []int64{}
	for i, id := range tileIDs {
		isLast := i == len(tileIDs)-1
		tiles, ok := lookup(gid)
		if !ok {
			break
		}
		t, ok := tiles[id]
		if !ok {
			// Unknown id — stale or bogus. Keep the current grid and
			// continue with the next id.
			continue
		}
		switch {
		case t.IsWell:
			path = append(path, id)
			gid, _ = strconv.ParseInt(t.ChildGridID, 10, 64)
		case t.IsContent:
			if !isLast {
				// Content tile mid-path is nonsense; ignore and keep walking.
				continue
			}
			fileTileID = id
		}
	}
	return path, fileTileID
}
