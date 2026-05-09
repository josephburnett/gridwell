// Package zoomtrans computes the (Cx, Cy, Zoom) endpoints for the
// "zoom into / out of a well" transition animation used by the Ascent
// client.
//
// The math is delicate enough that it deserves an isolated, unit-tested
// home rather than living inline in render.go. The visual goal is a
// continuous zoom: at the moment of switching from "viewing parent grid"
// to "viewing child grid", a child cell rendered as a well preview
// (scaled by 1/previewFactor inside the well's footprint) must match the
// pixel size of a child cell rendered natively in the child grid view.
//
// Tunable: previewFactor is how many times smaller a child cell is in
// the well's preview than a parent cell. Bigger previewFactor → finer
// preview, longer descent zoom. The same factor governs ascent.
package zoomtrans

// Endpoints describes one end of a transition: the pane state expressed
// as descent path, viewport center in cells (sub-cell precision), and
// zoom multiplier.
type Endpoints struct {
	Path    []int64
	Cx, Cy  float64
	Zoom    float64
}

// Well is the minimal information about a well needed to compute a
// transition: its row id, its location and size in the parent grid, and
// its saved internal view offset.
type Well struct {
	ID    int64
	X, Y  int64
	W, H  int64
	ViewX int64
	ViewY int64
}

// PreviewFactor is the scale at which a well shows its child grid.
// One child cell renders at (parent_cell_size / PreviewFactor) pixels
// inside the well's footprint.
const PreviewFactor = 8.0

// Descent computes the transition endpoints for descending from the
// given parent state through the given well into the well's child grid.
//
// The animation interpolates Cx, Cy, Zoom from `from` to `mid`. At t=1,
// the pane atomically switches to `to` (which is in the child grid
// coordinate space). The switch is calibrated to be visually continuous.
func Descent(from Endpoints, w Well) (mid, to Endpoints) {
	wellCx := float64(w.X) + float64(w.W)/2
	wellCy := float64(w.Y) + float64(w.H)/2
	mid = Endpoints{
		Path: from.Path,
		Cx:   wellCx,
		Cy:   wellCy,
		Zoom: from.Zoom * PreviewFactor,
	}
	childPath := append(append([]int64(nil), from.Path...), w.ID)
	to = Endpoints{
		Path: childPath,
		// Child grid viewport: center on the well's saved view region.
		Cx:   float64(w.ViewX) + float64(w.W)/2,
		Cy:   float64(w.ViewY) + float64(w.H)/2,
		// Calibrated zoom: at the moment of switch, the parent cell
		// pixel size is from.Zoom*PreviewFactor*64; the preview cell
		// is that divided by PreviewFactor, equal to from.Zoom*64.
		// To match, set child Zoom = from.Zoom.
		Zoom: from.Zoom,
	}
	return
}

// Ascent computes the transition endpoints for ascending from the child
// grid back into the parent grid, given the parent's well that we
// descended through.
//
// `from` is the current (child-grid) pane state. `mid` is the animated
// end-of-zoom-out target in child-grid coords. `to` is the state to
// install after the switch, in the parent grid. The math is the inverse
// of Descent.
func Ascent(from Endpoints, w Well, parentPath []int64) (mid, to Endpoints) {
	mid = Endpoints{
		Path: from.Path,
		Cx:   float64(w.ViewX) + float64(w.W)/2,
		Cy:   float64(w.ViewY) + float64(w.H)/2,
		Zoom: from.Zoom / PreviewFactor,
	}
	to = Endpoints{
		Path: parentPath,
		Cx:   float64(w.X) + float64(w.W)/2,
		Cy:   float64(w.Y) + float64(w.H)/2,
		Zoom: from.Zoom,
	}
	return
}
