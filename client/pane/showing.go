package pane

import (
	"slices"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/dragdrop"
)

// Showing is the grids this client shows, sorted, each once: the one owner of
// what it tells the node (SetInterest). A laid-out pane shows the grid it is
// at, and a pane standing in a content tile shows the grid that tile sits in,
// since Anchor and Path leave the content frame out. A well on a pane's screen
// shows its child grid too, one level deep, because the renderer draws that
// grid as its preview. A parked level's tree is off screen and is not t. leaf
// resolves a place to its grid id, "" when it cannot yet; tiles is a grid's
// cached rows, nil when it is not cached.
func Showing(t *Tree, laidOut map[string]Rect, leaf func(anchor string, path []string) string, tiles func(grid string) map[string]*gridwellv1.Tile) []string {
	var out []string
	t.Walk(func(p *Pane) {
		r, ok := laidOut[p.ID]
		if !ok {
			return
		}
		g := leaf(p.Anchor(), p.Path())
		if g == "" {
			return
		}
		out = append(out, g)
		if p.ContentID() != "" {
			return
		}
		ps := p.Screen(r)
		for _, n := range tiles(g) {
			if n.Kind == rpc.KindWell && n.ChildGridId != "" && ps.Shows(float64(n.X), float64(n.Y), float64(n.W), float64(n.H)) {
				out = append(out, n.ChildGridId)
			}
		}
	})
	slices.Sort(out)
	return slices.Compact(out)
}

// Screen is p laid out at r, in the shape dragdrop measures.
func (p *Pane) Screen(r Rect) dragdrop.Pane {
	return dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: CellPx,
	}
}
