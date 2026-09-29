package pane

import "slices"

// Showing is the grids this client shows, sorted, each once: the one owner of
// what it tells the node (SetInterest). A laid-out pane shows the grid it is
// at, and a pane standing in a content tile shows the grid that tile sits in,
// since Anchor and Path leave the content frame out. A parked level's tree is
// off screen and is not t. leaf resolves a place to its grid id, "" when it
// cannot yet.
func Showing(t *Tree, laidOut map[string]Rect, leaf func(anchor string, path []string) string) []string {
	var out []string
	t.Walk(func(p *Pane) {
		if _, ok := laidOut[p.ID]; !ok {
			return
		}
		if g := leaf(p.Anchor(), p.Path()); g != "" {
			out = append(out, g)
		}
	})
	slices.Sort(out)
	return slices.Compact(out)
}
