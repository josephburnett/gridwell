// Package zoomtrans computes the (Cx, Cy, Zoom) endpoints for the
// "zoom into / out of a well" transition animation used by the Gridwell
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

import (
	"math"
	"slices"
)

// Endpoints describes one end of a transition: the pane state expressed
// as descent path, viewport center in cells (sub-cell precision), and
// zoom multiplier.
type Endpoints struct {
	Path   []string
	Cx, Cy float64
	Zoom   float64
}

// Well is the minimal information about a well needed to compute a
// transition: its row id, its location and size in the parent grid, and
// the framing it was left at — a float CENTER in the child grid's
// coordinates (ViewCx/ViewCy) plus the intrinsic view ratio.
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
	ID       string
	X, Y     int64
	W, H     int64
	ViewCx   float64
	ViewCy   float64
	ViewZoom float64
}

// PreviewFactor is the scale at which a well shows its child grid.
// One child cell renders at (parent_cell_size / PreviewFactor) pixels
// inside the well's footprint. Used by the renderer to draw previews;
// also governs the calibrated zoom at the path-switch moment so that
// previews and the just-switched-to view render at identical scale.
const PreviewFactor = 8.0

// DefaultWellViewZoom is the intrinsic ratio used in place of a well's
// stored ViewZoom when it is 0 ("never visited"). Picked so the legacy
// PreviewFactor calibration falls out of the standard LiveFromIntrinsic
// formula: live = (1/PreviewFactor) × Overtake = Overtake / PreviewFactor.
const DefaultWellViewZoom = 1.0 / PreviewFactor

// EffectiveViewZoom returns stored if positive, else fallback. This is
// the one place "unvisited tile" branching lives — every read site
// passes its tile's stored ViewZoom and the appropriate default,
// then feeds the result into LiveFromIntrinsic or directly into a
// preview-cell formula (parentCell × ratio).
//
// For wells, fallback is DefaultWellViewZoom. For files, fallback is
// caller-supplied because the initial reading scale is pane-dependent.
func EffectiveViewZoom(stored, fallback float64) float64 {
	if stored > 0 {
		return stored
	}
	return fallback
}

// WheelZoom computes the new viewport zoom and center for a wheel zoom
// centered on the cursor, keeping the world point under the cursor fixed on
// screen (map-style). deltaY is the raw wheel delta; the per-event step is
// scaled (÷200) and capped at ±0.5 so a fast scroll covers more range
// without jumping. (cellX, cellY) is the world cell under the cursor;
// (cx, cy) the current viewport center; factorBase the per-notch zoom factor.
// The new zoom is clamped to [zMin, zMax]; when the clamp pins zoom unchanged
// the center doesn't move (no drift at the limits).
func WheelZoom(deltaY, oldZoom, cx, cy, cellX, cellY, factorBase, zMin, zMax float64) (zoom, newCx, newCy float64) {
	step := deltaY / 200.0
	if step > 0.5 {
		step = 0.5
	}
	if step < -0.5 {
		step = -0.5
	}
	z := oldZoom * math.Pow(factorBase, -step*4)
	if z < zMin {
		z = zMin
	}
	if z > zMax {
		z = zMax
	}
	if z == oldZoom {
		return z, cx, cy
	}
	ratio := oldZoom / z
	return z, cellX - (cellX-cx)*ratio, cellY - (cellY-cy)*ratio
}

// WellWheelView advances a hover-wheel zoom of a well's stored preview
// framing (issue #210) by one notch, cursor-anchored: the child-grid point
// under the cursor stays under the cursor, so zooming drifts the view
// toward the cursor (issue #219 — small navigation by zooming). The center
// is FLOAT in, out, and all the way to the store (schema v11), so the
// sub-cell drift a wheel burst accumulates survives the save; the old
// integer window ORIGIN rounded it away, which was the #219 bug.
// cx0/cy0 <= 0 sentinel is not used; pass the well's stored center for
// the first notch. changed=false when the clamp pinned the
// ratio or the preview is degenerate; a no-op wheel never mutates.
func WellWheelView(deltaY float64, w Well, parentCell, cursorDxPx, cursorDyPx, cx0, cy0, factorBase, rMin, rMax float64) (cx1, cy1, ratio float64, changed bool) {
	r0 := EffectiveViewZoom(w.ViewZoom, DefaultWellViewZoom)
	previewCell := parentCell * r0
	if previewCell <= 0 {
		return cx0, cy0, w.ViewZoom, false
	}
	px := cx0 + cursorDxPx/previewCell
	py := cy0 + cursorDyPx/previewCell
	r1, c1x, c1y := WheelZoom(deltaY, r0, cx0, cy0, px, py, factorBase, rMin, rMax)
	if r1 == r0 {
		return cx0, cy0, w.ViewZoom, false
	}
	return c1x, c1y, r1, true
}

// Overtake returns the zoom at which a (footprintW × footprintH) cell
// footprint exceeds both dimensions of a (refW × refH) px reference
// rectangle — the "footprint has fully consumed the reference area, its
// outline is just past every edge" point. Uses the larger of the two
// dim ratios so neither dimension can fit; the descent target "fills"
// the rect (one dim exactly, the other overflows).
//
// Wells use Overtake: the well's descent target is "well fills pane".
// Files use Fit (min of ratios) — see below — because the file
// footprint should match its grey area, calibrated by the bounding
// dimension that limits user content. Returns 1 on degenerate input.
func Overtake(footprintW, footprintH int64, refW, refH, cellPx float64) float64 {
	if footprintW <= 0 || footprintH <= 0 || cellPx <= 0 {
		return 1
	}
	zw := refW / (float64(footprintW) * cellPx)
	zh := refH / (float64(footprintH) * cellPx)
	return math.Max(zw, zh)
}

// Fit returns the zoom at which a (footprintW × footprintH) cell
// footprint exactly fits inside a (refW × refH) px reference
// rectangle — one dim matches exactly, the other has slack. Uses the
// smaller of the two dim ratios.
//
// Used by files for fileOvertakeZoom. Calibrating against the smaller
// inner-box dimension means the saved ViewZoom reconstructs the text
// scale relative to whichever dim was binding the user's editing
// (the user's content can only grow until it hits the smaller dim;
// past that they would have wanted a bigger file footprint instead).
// The preview then renders text at a scale that fills the file cell
// in that same binding dimension.
//
// Returns 1 on degenerate input.
func Fit(footprintW, footprintH int64, refW, refH, cellPx float64) float64 {
	if footprintW <= 0 || footprintH <= 0 || cellPx <= 0 {
		return 1
	}
	zw := refW / (float64(footprintW) * cellPx)
	zh := refH / (float64(footprintH) * cellPx)
	return math.Min(zw, zh)
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
// Three endpoints in transition order:
//   - mid: parent-grid state at the end of segment A (pan-and-zoom to
//     well center at Overtake).
//   - swap: child-grid state immediately after the atomic path swap.
//     Calibrated for visual continuity: just-before previewCell ==
//     just-after liveCell.
//   - final: child-grid state at the end of segment C (ease-out to the
//     well's saved view ratio). Equal to swap unless from.Zoom > Overtake.
//
// Returning final from this function (rather than reconstructing it at
// the call site) ensures the saved-ratio → live-zoom formula lives in
// exactly one place. A round-trip bug from the call site recomputing
// this independently is structurally impossible.
//
// `paneW`, `paneH` are the pixel dimensions of the pane the descent is
// happening in; `cellPx` is the screen pixel size of one cell at zoom
// 1.0. These determine the target zoom needed to make the well overtake
// the pane in both dimensions.
func Descent(from Endpoints, w Well, paneW, paneH, cellPx float64) (mid, swap, final Endpoints) {
	wellCx := float64(w.X) + float64(w.W)/2
	wellCy := float64(w.Y) + float64(w.H)/2
	overtake := OvertakeZoom(w, paneW, paneH, cellPx)
	// Make sure the descent always zooms in, even if the user was already
	// past the overtake zoom.
	zPTarget := overtake
	if zPTarget < from.Zoom {
		zPTarget = from.Zoom
	}
	mid = Endpoints{
		Path: from.Path,
		Cx:   wellCx,
		Cy:   wellCy,
		Zoom: zPTarget,
	}
	childPath := append(slices.Clone(from.Path), w.ID)
	// Calibrated child zoom at the swap moment: live = ratio × zPTarget.
	// With DefaultWellViewZoom substituted for an unvisited well
	// (1/PreviewFactor), this collapses to zPTarget/PreviewFactor — the
	// legacy calibration. Preview and live render at the same cell size
	// across the path swap, in both cases.
	ratio := EffectiveViewZoom(w.ViewZoom, DefaultWellViewZoom)
	swapZoom := LiveFromIntrinsic(ratio, zPTarget)
	swap = Endpoints{
		Path: childPath,
		Cx:   w.ViewCx,
		Cy:   w.ViewCy,
		Zoom: swapZoom,
	}
	// Final state: live zoom reconstructed from the *real* overtake (not
	// zPTarget). When the user started past Overtake, zPTarget == from.Zoom
	// and swapZoom is higher than the saved-ratio target; segment C
	// eases out. When zPTarget == overtake, swap == final and segment C
	// is a no-op.
	final = swap
	final.Zoom = LiveFromIntrinsic(ratio, overtake)
	return
}

// StoredView is the live framing a well's persisted view describes — the
// same numbers Descent's `final` lands on, without the transition. The
// ONE read-side conversion for "what did the user leave this grid at":
// boot and the no-session-state ascent fallbacks read it, so a reload
// never lands a pane on a framing the user didn't set (the guiding rule
// applied on the way OUT, not just the way in).
func StoredView(w Well, paneW, paneH, cellPx float64) (cx, cy, zoom float64) {
	ratio := EffectiveViewZoom(w.ViewZoom, DefaultWellViewZoom)
	return w.ViewCx, w.ViewCy,
		LiveFromIntrinsic(ratio, OvertakeZoom(w, paneW, paneH, cellPx))
}

// Ascent computes the transition endpoints for ascending from the child
// grid back into the parent grid, given the parent's well that we
// descended through. The animation interpolates from `from` to `mid` in
// child grid coords, then atomically switches to `to` in parent coords.
// Same calibration rule as Descent, in reverse: at the switch the
// child's rendered cell size matches the parent's preview cell size.
func Ascent(from Endpoints, w Well, parentPath []string, paneW, paneH, cellPx float64) (mid, to Endpoints) {
	zPTarget := OvertakeZoom(w, paneW, paneH, cellPx)
	// Mid-state child zoom: matches the preview cell size so the
	// path-swap into the parent's preview is continuous (mirrors
	// Descent). Same default substitution as Descent for unvisited.
	ratio := EffectiveViewZoom(w.ViewZoom, DefaultWellViewZoom)
	midZoom := LiveFromIntrinsic(ratio, zPTarget)
	mid = Endpoints{
		Path: from.Path,
		Cx:   w.ViewCx,
		Cy:   w.ViewCy,
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

// PanDist returns the pan motion distance in screen pixels for a (dx,
// dy) delta given in cell units, at the provided zoom level and base
// cell size. Used by the descent / ascent animation timing math.
func PanDist(dx, dy, zoom, cellPx float64) float64 {
	return math.Hypot(dx, dy) * cellPx * zoom
}

// ZoomDist returns the log-zoom distance scaled to the same
// px-equivalent units PanDist returns, so the two can be added when
// blending pan + zoom into a single animation duration. factor is the
// log-zoom-to-pixel weighting; the renderer uses 4 so zoom phases get
// the bulk of an animation's time.
//
// Either zoom <= 0 makes the result 0 — degenerate, no motion.
func ZoomDist(z1, z2, cellPx, factor float64) float64 {
	if z1 <= 0 || z2 <= 0 {
		return 0
	}
	return math.Abs(math.Log(z2/z1)) * cellPx * factor
}
