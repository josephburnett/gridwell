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
// its saved internal view offset and intrinsic view ratio.
//
// ViewZoom is an intrinsic ratio = liveScale / EffectiveOvertake at the
// moment of the user's last ascent from this well/file. It is
// dimensionless and window-independent: the preview formula uses it
// directly as the per-parentCell child-cell-size multiplier, and the
// descent formula reconstructs the user's live zoom for the current
// pane size as ViewZoom × EffectiveOvertake_now. Zero means "never
// visited"; in that case the descent and preview fall back to the
// PreviewFactor calibration.
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

// Overtake returns the zoom at which a (footprintW × footprintH) cell
// footprint exceeds both dimensions of a (refW × refH) px reference
// rectangle — the "footprint has fully consumed the reference area, its
// outline is just past every edge" point.
//
// This is the path-swap target zoom shared by wells and files. For wells
// the reference rect is the pane; for files it is the inner-box (textarea
// region). Returns 1 on degenerate input so the descent never divides by
// zero downstream.
func Overtake(footprintW, footprintH int64, refW, refH, cellPx float64) float64 {
	if footprintW <= 0 || footprintH <= 0 || cellPx <= 0 {
		return 1
	}
	zw := refW / (float64(footprintW) * cellPx)
	zh := refH / (float64(footprintH) * cellPx)
	return math.Max(zw, zh)
}

// OvertakeZoom is the well-flavored convenience for Overtake — takes a
// Well so existing callers stay terse. Files call Overtake directly with
// the inner-box dimensions as the reference rect.
func OvertakeZoom(w Well, paneW, paneH, cellPx float64) float64 {
	return Overtake(w.W, w.H, paneW, paneH, cellPx)
}

// LiveFromIntrinsic reconstructs a live zoom from an intrinsic ratio
// and the current effective overtake. This is the inverse of
// IntrinsicFromLive — together they are the single source of truth for
// the "stored ratio ↔ live zoom" mapping.
//
// Returns 0 when viewZoom is 0, so callers can detect "never visited"
// and substitute a pane-calibrated default.
func LiveFromIntrinsic(viewZoom, overtake float64) float64 {
	return viewZoom * overtake
}

// IntrinsicFromLive converts a live zoom to the dimensionless intrinsic
// ratio that, multiplied by overtake_now, reconstructs it. Returns 0 on
// degenerate input so the caller can leave ViewZoom unset.
func IntrinsicFromLive(liveZoom, overtake float64) float64 {
	if overtake <= 0 || liveZoom <= 0 {
		return 0
	}
	return liveZoom / overtake
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
	// Calibrated child zoom at the switch moment: at parent =
	// OvertakeZoom, a parent cell is cellPx × OvertakeZoom in screen
	// px, and the renderer paints child cells at parentCell × ViewZoom
	// in the preview. So for the path-swap to be visually continuous,
	// the post-swap child zoom must be ViewZoom × OvertakeZoom (so
	// child cells render at cellPx × ViewZoom × OvertakeZoom in both
	// preview-just-before and live-just-after). ViewZoom == 0 falls
	// back to the legacy PreviewFactor calibration (1/PreviewFactor as
	// the implicit default ratio).
	childZoom := LiveFromIntrinsic(w.ViewZoom, zPTarget)
	if childZoom <= 0 {
		childZoom = zPTarget / PreviewFactor
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
	// is continuous. With ViewZoom = intrinsic ratio, the matching
	// child zoom is ViewZoom × OvertakeZoom_now (mirrors Descent).
	midZoom := LiveFromIntrinsic(w.ViewZoom, zPTarget)
	if midZoom <= 0 {
		midZoom = zPTarget / PreviewFactor
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
