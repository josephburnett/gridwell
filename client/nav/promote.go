package nav

import (
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/scratch"
)

// The promote verb: an ephemeral url visit dragged onto a grid becomes a
// persistent tile there and the visiting pane follows its content. The create
// is the dispatcher's, so this plans what happens once the row exists, under
// the same moved-on guard every async gesture wears.
func (m *Machine) promote(g Gesture, w World) Plan {
	var pl planner
	// The origin pane must still be showing the visit being promoted.
	still := Guard{Kind: GuardDescendedIn, PaneID: g.PaneID, TileID: g.OldID}
	op, ok := w.Pane(g.PaneID)
	dp, destOK := w.Pane(g.DestPaneID)
	if !ok || !destOK || !still.holds(w) {
		// Moved on mid-flight; the tile stays where it was dropped.
		return pl.plan()
	}
	created := g.Created
	// The view's final frame, title and trail freeze onto the new tile, never
	// the row about to die.
	pl.add(Effect{Kind: EffCloseStream, PaneID: op.ID, Streams: StreamURL, Freeze: true,
		FreezeOnto: &FreezeTarget{TileID: created.Id, GridID: created.GridId}})
	// The pane follows its content: the visit's frame is replaced by one on
	// the destination's stack, minted by pane.ContentFrame at the zoom a
	// descent would have landed on, so the promoted pane is a descended pane
	// with a real overtake to zoom out from. No zoom floor: a promote has no
	// prior grid zoom in this pane.
	pl.add(Effect{Kind: EffRelocatePane, PaneID: op.ID, DestPaneID: dp.ID,
		TileID: created.Id,
		Foot:   pane.Footprint{X: created.X, Y: created.Y, W: created.W, H: created.H},
		Zoom:   panebox.FitZoom(op.Rect, created.W, created.H, w.TextSideInset, w.CellPx)})
	// The content scale follows the frame, as at every descent and landing.
	pl.add(Effect{Kind: EffScaleContent, PaneID: op.ID})
	pl.add(Effect{Kind: EffPlaceURLView, PaneID: op.ID, TileID: created.Id, Tile: created})
	// The row dies only if known ephemeral and no sibling pane still shows
	// the visit; a split clone deletes it on its own ascent. The same rule
	// the ascent applies, retired after the relocate so the layout flushed
	// first names the new tile.
	if old := w.Promote.old(); old != nil {
		eph, known := scratch.Ephemeral(op.Scratch, old.GridId)
		if eph && known && !w.otherPaneShows(op.ID, old.Id) {
			m.retireVisit(old, &pl)
		}
	}
	pl.add(Effect{Kind: EffRefreshOverlay})
	pl.add(Effect{Kind: EffScheduleURLUpdate})
	return pl.plan()
}
