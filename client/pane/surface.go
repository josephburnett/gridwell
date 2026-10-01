package pane

// SurfaceVerdict is one frame's sweep for one live surface.
type SurfaceVerdict int

const (
	// SurfacePark: the pane sits in a stacked level, which stays alive, so the
	// surface keeps running with nowhere to be drawn.
	SurfacePark SurfaceVerdict = iota
	// SurfaceOrphan: on screen, but no longer inside the descent this surface
	// was opened for.
	SurfaceOrphan
	SurfaceShow // still in that descent: track the content box
)

// SurfaceOf answers it for one surface. descentID is the pane frame the
// surface was opened for, not always the tile it shows: a live link's surface
// belongs to the link's target while the frame carries the link row, so
// passing the target's id would orphan it every frame.
func SurfaceOf(onScreen bool, paneContentID, descentID string) SurfaceVerdict {
	if !onScreen {
		return SurfacePark
	}
	if paneContentID == "" || paneContentID != descentID {
		return SurfaceOrphan
	}
	return SurfaceShow
}

// CanvasGesture mirrors one frame's gesture and overlay state. A live surface
// paints over the canvas and swallows the mouse over its rect, so which
// surfaces a gesture parks is decided per pane: a gesture in one pane cannot
// blank the others.
type CanvasGesture struct {
	// DragPane is the pane a drag was armed in, "" when none is. Until it makes
	// a ghost only its own pane is under it.
	DragPane string
	// Ghost follows the pointer into any pane and outlives the release through
	// the snap animation.
	Ghost bool
	// TileResize is a right-drag sizing one tile. Its dashed footprint is
	// stroked unclipped, so a tile bigger than its pane reaches a neighbour.
	TileResize bool
	// PaneGesture is a right-drag swap or split, whose preview names the pane
	// under the cursor.
	PaneGesture bool
	// PaneResize: half the divider's grab band is a surface's own rect
	// (live-border-drag.spec.ts).
	PaneResize bool
	// MenuOpen: the + palette is anchored to the bar, so it floats over the
	// pane below its own (menu-over-live-pane.spec.ts).
	MenuOpen  bool
	ModalOpen bool
}

// ParkSurface reports whether the live surface in paneID goes off screen this
// frame.
func ParkSurface(g CanvasGesture, paneID string) bool {
	if g.Ghost || g.TileResize || g.PaneGesture || g.PaneResize || g.MenuOpen || g.ModalOpen {
		return true
	}
	// A pan, and a drag before its ghost, reach one pane.
	return g.DragPane != "" && g.DragPane == paneID
}

// CanvasOwnsPointer reports whether the canvas keeps every pointer event this
// frame: an armed gesture must hear its own release, even over a surface it
// did not park.
func CanvasOwnsPointer(g CanvasGesture) bool {
	// ParkSurface with no pane names the arms that reach every pane.
	return g.DragPane != "" || ParkSurface(g, "")
}
