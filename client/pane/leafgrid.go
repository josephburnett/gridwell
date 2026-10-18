package pane

// ResolveLeafGrid walks a descent path from rootGridID, following each door
// tile's child grid. Where lookup reports the grid uncached or the path id
// absent it returns the last good grid id, so a stale prefix resolves as deep
// as it can and never past a gap — a slip here sends the pane to the wrong
// grid. The wasm caller reads the cache inside lookup and may kick a
// background fetch on a miss.
func ResolveLeafGrid(rootGridID string, path []string,
	lookup func(gid, doorID string) (childGridID string, gridCached, tileFound bool)) string {
	if rootGridID == "" {
		return ""
	}
	gid := rootGridID
	for _, doorID := range path {
		child, cached, found := lookup(gid, doorID)
		if !cached || !found {
			return gid
		}
		gid = child
	}
	return gid
}
