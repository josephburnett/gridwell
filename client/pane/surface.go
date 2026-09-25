package pane

// A live surface belongs to one pane and to the descent that opened it.
// Whether it still belongs on screen is one question, and both surface kinds
// ask it here.

// SurfaceVerdict is one frame's sweep for one live surface.
type SurfaceVerdict int

const (
	// SurfacePark: the pane sits in a stacked level, which stays alive, so the
	// surface keeps running with nowhere to be drawn.
	SurfacePark SurfaceVerdict = iota
	// SurfaceOrphan: on screen, but no longer inside the descent this surface
	// was opened for. What each kind does differs; the verdict does not.
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

// A live surface — a native WebContentsView for a url, an xterm host div for a
// shell — paints over the canvas and swallows the mouse over its own rect.
// Parking it takes it off screen for a frame. Which surfaces a gesture parks is
// decided here, per pane, so a gesture in one pane cannot blank the surfaces in
// the others.

// CanvasGesture mirrors one frame's gesture and overlay state. Each field is
// one armed thing; what each one reaches is ParkSurface's.
type CanvasGesture struct {
	// DragPane is the pane a left- or right-button drag was armed in, "" when
	// none is armed. Until it makes a ghost it has painted nothing and moved
	// nowhere, so only its own pane is under it.
	DragPane string
	// Ghost is a floating tile the canvas paints under the pointer. It follows
	// the pointer into any pane, the drop can land there, and it outlives the
	// release through the snap animation.
	Ghost bool
	// TileResize is a right-drag sizing one tile. Its dashed footprint is
	// stroked unclipped, so a tile bigger than its pane reaches a neighbour.
	TileResize bool
	// PaneGesture is a right-drag swap or split, whose preview names the pane
	// under the cursor rather than the one it started in.
	PaneGesture bool
	// PaneResize is a left-drag on a divider. The grab band is the gutter
	// between two content boxes, so half of it is a surface's own rect;
	// live-border-drag.spec.ts is that press.
	PaneResize bool
	// MenuOpen is the + palette. It is anchored to the bar, so it floats over
	// the pane below the one it belongs to; see menu-over-live-pane.spec.ts.
	MenuOpen bool
	// ModalOpen is the url modal, a DOM card a live surface would paint over.
	ModalOpen bool
}

// ParkSurface reports whether the live surface in paneID goes off screen this
// frame.
func ParkSurface(g CanvasGesture, paneID string) bool {
	if g.Ghost || g.TileResize || g.PaneGesture || g.PaneResize || g.MenuOpen || g.ModalOpen {
		return true
	}
	// A pan, and a drag before its ghost, reach one pane. That pane holds no
	// live surface today — a content descent swallows the press that arms one
	// — so this parks nothing, and it stays the rule rather than the inference.
	return g.DragPane != "" && g.DragPane == paneID
}

// CanvasOwnsPointer reports whether the canvas keeps every pointer event this
// frame, whatever it lands over. An armed gesture must hear its own release,
// and a surface this gesture did not park is still not entitled to eat it.
func CanvasOwnsPointer(g CanvasGesture) bool {
	// ParkSurface with no pane names the arms that reach every pane, so one
	// added there cannot be missed here.
	return g.DragPane != "" || ParkSurface(g, "")
}
