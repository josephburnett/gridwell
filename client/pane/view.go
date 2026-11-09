package pane

import (
	"slices"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// View is what this client tells the node it shows (SetView), the one owner of
// its shape: each pane on screen in tree order, with its place as the URL
// names it, the grid it resolves to, and whether it has focus; the workspace tiles
// the window is inside; label; and Showing, with strip, the templates grid
// while a + menu's strip shows it, else "". leaf and tiles are Showing's.
func View(t *Tree, levels *Levels, laidOut map[string]Rect, label string, leaf func(anchor string, path []string) string, tiles func(grid string) map[string]*gridwellv1.Tile, strip string) *gridwellv1.SetViewRequest {
	shown := Showing(t, levels, laidOut, leaf, tiles)
	if strip != "" && !slices.Contains(shown, strip) {
		shown = append(shown, strip)
		slices.Sort(shown)
	}
	v := &gridwellv1.SetViewRequest{
		Label:          label,
		WorkspaceTiles: levels.tileIDs(),
		ShownGridIds:   shown,
	}
	t.Walk(func(p *Pane) {
		if _, ok := laidOut[p.ID]; !ok {
			return
		}
		anchor, path := p.Anchor(), p.Path()
		v.Panes = append(v.Panes, &gridwellv1.ViewPane{
			Id:        p.ID,
			Anchor:    anchor,
			Path:      path,
			ContentId: p.ContentID(),
			GridId:    leaf(anchor, path),
			Focused:   p.ID == t.Focus,
		})
	})
	return v
}
