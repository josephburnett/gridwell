package nav

import (
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/scratch"
)

// The promote verb: an ephemeral url visit dragged off the bar's crumb onto a
// grid becomes a persistent tile there, and the visiting pane follows its
// content.
//
// The create is the shim's — it is a mutation, and the dispatcher owns
// mutations — so this plans what happens once the row exists. That makes it an
// ordinary continuation of an async gesture, and it wears the same guard every
// other one does: the user may have ascended, closed the pane, or descended
// somewhere else while the create was in flight, and where they went is never
// overridden.
func (m *Machine) promote(g Gesture, w World) Plan {
	var pl planner
	// The origin pane must still be showing the visit being promoted. This is
	// pane.StillDescended, spelled the one way the machine spells it.
	still := Guard{Kind: GuardDescendedIn, PaneID: g.PaneID, TileID: g.OldID}
	op, ok := w.Pane(g.PaneID)
	dp, destOK := w.Pane(g.DestPaneID)
	if !ok || !destOK || !still.holds(w) {
		// Moved on mid-flight: the tile stays where it was dropped.
		return pl.plan()
	}
	created := g.Created
	// The view's final frame, title and trail freeze onto the NEW tile, never
	// the row about to die.
	pl.add(Effect{Kind: EffCloseStream, PaneID: op.ID, Streams: StreamURL, Freeze: true,
		FreezeOnto: &FreezeTarget{TileID: created.ID, GridID: created.GridID}})
	// The row dies only if it is known ephemeral and no sibling pane still
	// shows the visit; a split clone keeps it and deletes it on its own
	// ascent. The same rule the ascent applies, from the same two owners.
	if old := w.Promote.old(); old != nil {
		eph, known := scratch.Ephemeral(op.Scratch, old.GridID)
		if eph && known && !w.otherPaneShows(op.ID, old.ID) {
			pl.add(Effect{Kind: EffDeleteEphemeral, GridID: old.GridID, TileID: old.ID})
		}
	}
	// The pane follows its content: RelocateTo replaces the visit's frame with
	// one on the destination's stack, so the next ascent lands where the tile
	// now lives. The frame is minted by pane.ContentFrame — the constructor a
	// descent uses — at the zoom a descent into this tile would have landed
	// on, so the promoted pane is a descended pane and its ascent has a real
	// overtake to zoom out from. There is no zoom floor here: a promote has no
	// prior grid zoom in this pane to refuse to zoom out past.
	pl.add(Effect{Kind: EffRelocatePane, PaneID: op.ID, DestPaneID: dp.ID,
		TileID: created.ID,
		Foot:   pane.Footprint{X: created.X, Y: created.Y, W: created.W, H: created.H},
		Zoom:   panebox.FitZoom(op.Rect, created.W, created.H, w.TextSideInset, w.CellPx)})
	// The content scale follows the frame, as it does at the end of every
	// descent and every ascent landing (issue #82).
	pl.add(Effect{Kind: EffScaleContent, PaneID: op.ID})
	// And the page goes live again on the new tile.
	pl.add(Effect{Kind: EffPlaceURLView, PaneID: op.ID, TileID: created.ID, Tile: created})
	pl.add(Effect{Kind: EffRefreshOverlay})
	pl.add(Effect{Kind: EffScheduleURLUpdate})
	return pl.plan()
}
