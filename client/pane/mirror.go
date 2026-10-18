package pane

import "slices"

// Shown is a content tile drawn in a pane: a tile of the grid the pane is at, a
// link's target, or the tile it is descended into, by rpc.ContentID.
type Shown struct {
	PaneID    string
	ContentID string
}

// Mirrored names the live surfaces whose preview another pane shows, sorted: the
// ones that must keep refreshing the shared preview cache. A pane shows every
// tile of its grid, scrolled into view or not, so a pan never makes a preview
// stale and only the tree, navigation and a grid refetch change the answer.
// The surface's own pane reads its preview only while parked, and a parked
// surface has nothing to capture, so each kind captures at the park instead.
func Mirrored(live []Holder, shown []Shown) []string {
	var out []string
	for _, h := range live {
		if slices.ContainsFunc(shown, func(f Shown) bool {
			return f.ContentID == h.TileID && f.PaneID != h.PaneID
		}) {
			out = append(out, h.PaneID)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
