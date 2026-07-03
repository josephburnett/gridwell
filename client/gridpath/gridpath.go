// Package gridpath holds the pure navigation decisions over a pane's
// descent path: resolving a path to its leaf grid, and classifying how an
// ascent should play out. Both were inline in build-tagged wasm (never run
// by `go test`) though they decide where you land — a stale-prefix or
// off-by-one slip sends you to the wrong grid.
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

// AscentMode is how an ascent gesture should play out once the deepest
// resolvable ancestor level has been found.
type AscentMode int

const (
	// AscentToRoot: nothing in the path resolved — snap to root.
	AscentToRoot AscentMode = iota
	// AscentSnapToLevel: one or more levels were skipped (missing well row
	// or unreadable grid) — snap directly to the resolved level rather than
	// animate, since the animation math assumes ascending out of the leaf.
	AscentSnapToLevel
	// AscentAnimate: the leaf itself resolved — animate the normal ascent.
	AscentAnimate
)

// ClassifyAscent maps the deepest resolvable level (from a leaf-ward walk;
// -1 if none) and the path length to how the ascent should play. The
// resolvedLevel != pathLen-1 test is the off-by-one that decides
// animate-vs-snap, so it is pinned here.
func ClassifyAscent(resolvedLevel, pathLen int) AscentMode {
	switch {
	case resolvedLevel < 0:
		return AscentToRoot
	case resolvedLevel != pathLen-1:
		return AscentSnapToLevel
	default:
		return AscentAnimate
	}
}

// AscentWalk finds the deepest ancestor level an ascent can animate from: it
// walks back from the leaf and returns the first (deepest) level whose parent
// grid resolves and still contains the well row, or -1 when nothing on the
// path resolves. resolves(parentPath, wellID) answers for one level — the
// caller supplies the cache lookup (and any fetch side effect for a miss).
// Feed the result to ClassifyAscent. The walk order and slicing boundaries
// used to live inline in the wasm startAscent with no test; this is the
// ascent mirror of ResolveLeafGrid.
func AscentWalk(path []string, resolves func(parentPath []string, wellID string) bool) int {
	for level := len(path) - 1; level >= 0; level-- {
		if resolves(path[:level], path[level]) {
			return level
		}
	}
	return -1
}
