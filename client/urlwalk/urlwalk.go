// Package urlwalk holds the pure boot-time descent walk: resolving a
// URL's tile-id list against the user's grids into a descent path plus an
// optional trailing content tile. The state machine — skip missing ids,
// switch grids at well boundaries, stop at a content leaf — lives here, not
// in the wasm shim, so `go test` covers it. A misstep lands the user in the
// wrong grid.
package urlwalk

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
type GridLookup func(gid string) (tiles map[string]Tile, ok bool)

// Walk resolves tileIDs against the grids reachable from rootGridID and
// returns the descent path (well ids, in order) plus the trailing
// file-tile id ("" if the leaf is a grid, not a content tile).
//
// Rules (loose on input, by design — a bookmarked URL must degrade
// gracefully as the canvas changes underneath it):
//   - An id missing from the current grid is skipped; the walk stays in
//     the same grid and tries the next id.
//   - A well id is appended to the path and the walk descends into its
//     child grid.
//   - A content tile is accepted only as the last id; a content tile
//     mid-path is nonsense and skipped.
//   - A grid that fails to load ends the walk with what's resolved so far.
func Walk(rootGridID string, tileIDs []string, lookup GridLookup) (path []string, fileTileID string) {
	gid := rootGridID
	path = []string{}
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
			gid = t.ChildGridID
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
