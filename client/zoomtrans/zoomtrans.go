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

import "math"

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
// its saved internal view offset and zoom.
//
// ViewZoom is the user's last child-grid zoom for this well. Zero means
// "not yet set" — the descent uses the calibrated default (zPTarget /
// PreviewFactor) and the preview renders at PreviewFactor scale. When
// non-zero, descent lands at exactly ViewZoom and the parent preview
// renders child cells at cellPx × ViewZoom screen pixels (instead of
// parentCell / PreviewFactor) so the preview matches the user's last
// view and the path-swap is continuous without an extra zoom segment.
type Well struct {
	ID       int64
	X, Y     int64
	W, H     int64
	ViewX    int64
	ViewY    int64
	ViewZoom float64
}

// PreviewFactor is the scale at which a well shows its child grid.
// One child cell renders at (parent_cell_size / PreviewFactor) pixels
// inside the well's footprint. Used by the renderer to draw previews;
// also governs the calibrated zoom at the path-switch moment so that
// previews and the just-switched-to view render at identical scale.
const PreviewFactor = 8.0

// OvertakeZoom returns the parent zoom at which the well's footprint
// exceeds both pane dimensions — the "well has fully consumed the
// screen, its outline is just past every edge" point. This is the zoom
// the descent animation aims for, regardless of how zoomed-out the user
// started: zooming exactly to here fills the smaller dim, and slightly
// beyond pushes the longer dim's outline off-screen too.
func OvertakeZoom(w Well, paneW, paneH, cellPx float64) float64 {
	if w.W <= 0 || w.H <= 0 || cellPx <= 0 {
		return 1
	}
	zw := paneW / (float64(w.W) * cellPx)
	zh := paneH / (float64(w.H) * cellPx)
	return math.Max(zw, zh)
}

// Descent computes the transition endpoints for descending from the
// given parent state through the given well into the well's child grid.
//
// The animation interpolates Cx, Cy, Zoom from `from` to `mid`. At t=1,
// the pane atomically switches to `to` (which is in the child grid
// coordinate space). The switch is calibrated to be visually continuous:
// at the switch moment, a child cell rendered as a preview (parent_cell
// / PreviewFactor) matches a child cell rendered native at the new
// zoom (the to.Zoom).
//
// `paneW`, `paneH` are the pixel dimensions of the pane the descent is
// happening in; `cellPx` is the screen pixel size of one cell at zoom
// 1.0. These determine the target zoom needed to make the well overtake
// the pane in both dimensions.
func Descent(from Endpoints, w Well, paneW, paneH, cellPx float64) (mid, to Endpoints) {
	wellCx := float64(w.X) + float64(w.W)/2
	wellCy := float64(w.Y) + float64(w.H)/2
	zPTarget := OvertakeZoom(w, paneW, paneH, cellPx)
	// Make sure the descent always zooms in, even if the user was already
	// past the overtake zoom.
	if zPTarget < from.Zoom {
		zPTarget = from.Zoom
	}
	mid = Endpoints{
		Path: from.Path,
		Cx:   wellCx,
		Cy:   wellCy,
		Zoom: zPTarget,
	}
	childPath := append(append([]int64(nil), from.Path...), w.ID)
	// Calibrated child zoom at the switch moment: must equal the
	// preview cell size in screen-px / cellPx so the path swap is
	// visually continuous. When the well has a stored ViewZoom, the
	// preview renders child cells at cellPx × ViewZoom (renderer rule
	// — see drawNodeWithPreview), so the matching child zoom is
	// ViewZoom directly. When unset (ViewZoom == 0), the original
	// PreviewFactor calibration applies.
	childZoom := zPTarget / PreviewFactor
	if w.ViewZoom > 0 {
		childZoom = w.ViewZoom
	}
	to = Endpoints{
		Path: childPath,
		Cx:   float64(w.ViewX) + float64(w.W)/2,
		Cy:   float64(w.ViewY) + float64(w.H)/2,
		Zoom: childZoom,
	}
	return
}

// Ascent computes the transition endpoints for ascending from the child
// grid back into the parent grid, given the parent's well that we
// descended through. The animation interpolates from `from` to `mid` in
// child grid coords, then atomically switches to `to` in parent coords.
// Same calibration rule as Descent, in reverse: at the switch the
// child's rendered cell size matches the parent's preview cell size.
func Ascent(from Endpoints, w Well, parentPath []int64, paneW, paneH, cellPx float64) (mid, to Endpoints) {
	zPTarget := OvertakeZoom(w, paneW, paneH, cellPx)
	// Mid-state child zoom: matches the preview cell size in
	// screen-px / cellPx so the path-swap into the parent's preview
	// is continuous. ViewZoom drives this when set (see Descent).
	midZoom := zPTarget / PreviewFactor
	if w.ViewZoom > 0 {
		midZoom = w.ViewZoom
	}
	mid = Endpoints{
		Path: from.Path,
		Cx:   float64(w.ViewX) + float64(w.W)/2,
		Cy:   float64(w.ViewY) + float64(w.H)/2,
		Zoom: midZoom,
	}
	// Make sure the ascent always zooms out from the user's current state.
	if mid.Zoom > from.Zoom {
		mid.Zoom = from.Zoom
	}
	// Parent's zoom at the switch is OvertakeZoom (the well filling
	// the pane). The just-after-switch parent zoom is independent of
	// ViewZoom because the preview cell size is fixed at cellPx ×
	// ViewZoom regardless of parent zoom — so any reasonable parent
	// zoom keeps the visual continuity.
	to = Endpoints{
		Path: parentPath,
		Cx:   float64(w.X) + float64(w.W)/2,
		Cy:   float64(w.Y) + float64(w.H)/2,
		Zoom: zPTarget,
	}
	return
}
