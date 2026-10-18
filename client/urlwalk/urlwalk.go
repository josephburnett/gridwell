// Package urlwalk resolves a URL's tile-id list against the user's grids into
// a descent path plus an optional trailing content tile. It is outside the
// wasm shim so go test covers it: a misstep lands the user in the wrong grid.
package urlwalk

// Tile is what a walk step needs. The caller classifies kinds with
// rpc.IsDoorKind and rpc.IsContentDescentKind, keeping wire types out of here.
type Tile struct {
	ChildGridID string
	IsDoor      bool
	IsContent   bool
}

// GridLookup returns the tiles of grid gid. It returns false when the grid
// cannot be loaded, and the walk stops where it is: a failed fetch never
// invents a path.
type GridLookup func(gid string) (tiles map[string]Tile, ok bool)

// Walk resolves tileIDs against the grids reachable from rootGridID and
// returns the descent path of door ids, plus the trailing file-tile id, "" when
// the leaf is a grid. The rules are loose on input so a bookmarked URL degrades
// as the canvas changes under it: a missing id or a content tile mid-path is
// skipped, and a grid that fails to load ends the walk with what resolved.
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
			continue
		}
		switch {
		case t.IsDoor:
			path = append(path, id)
			gid = t.ChildGridID
		case t.IsContent:
			if !isLast {
				continue
			}
			fileTileID = id
		}
	}
	return path, fileTileID
}
