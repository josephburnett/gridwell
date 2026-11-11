package nav

import (
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
)

// visitMoved follows a visit's row onto the grid its crumb was dropped on,
// once the move landed: the pane's place is re-rooted under the destination
// pane's, the frames from the row's up as they were, so the pane shows what it
// showed. The row keeps its id, so nothing is retired, a sibling pane on it
// keeps its place, and a live surface on it stays as it is. A workspace
// tile's visit is the open level, whose panes are its own: only the level
// follows the row.
func (m *Machine) visitMoved(g Gesture, w World) Plan {
	var pl planner
	row := g.Door
	if w.LevelTop != nil && w.LevelTop.TileID == row.GetId() {
		pl.add(Effect{Kind: EffLevelMoved, TileID: row.Id, GridID: row.GridId})
		return pl.plan()
	}
	op, ok := w.Pane(g.PaneID)
	dp, destOK := w.Pane(g.DestPaneID)
	level, holds := op.Stack.LevelOf(row.GetId())
	if !ok || !destOK || !holds {
		// Moved on mid-flight; the row stays where it was dropped.
		return pl.plan()
	}
	place := op.Stack.RelocatedOnto(dp.Stack, level, g.TileID,
		pane.Footprint{X: row.X, Y: row.Y, W: row.W, H: row.H},
		panebox.FitZoom(op.Rect, row.W, row.H, w.TextSideInset, w.CellPx))
	pl.install(op.ID, place, nil)
	// Only a content row on top changes the grid the place names.
	gridID := op.GridID
	if op.Stack.Content && level == op.Stack.Depth()-1 {
		gridID = row.GridId
	}
	m.landOnFrame(op.ID, place, gridID, w, &pl)
	pl.add(Effect{Kind: EffScheduleURLUpdate})
	return pl.plan()
}
