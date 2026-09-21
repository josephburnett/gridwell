// Package zoomtrans computes the (Cx, Cy, Zoom) endpoints of the zoom into and
// out of a well. At the path swap a child cell drawn as a preview and the same
// cell drawn natively are the same pixel size, so the zoom is continuous.
// PreviewFactor is the one tunable, for descent and ascent alike.
package zoomtrans

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"math"
	"slices"

	"github.com/josephburnett/gridwell/api/rpc"
)

// Endpoints is one end of a transition: descent path, viewport center in
// cells, zoom multiplier.
type Endpoints struct {
	Path   []string
	Cx, Cy float64
	Zoom   float64
}

// Well is a doorway's footprint plus the framing it was left at. ViewZoom is
// dimensionless, liveScale over the overtake at the last ascent, so a descent
// reconstructs the live zoom for the current pane size. ViewZoom 0 is the one
// "never visited" sentinel for the whole framing.
type Well struct {
	ID       string
	X, Y     int64
	W, H     int64
	ViewCx   float64
	ViewCy   float64
	ViewZoom float64
}

// WellOf reads a tile row as a Well. One derivation, so preview, descent and
// ascent cannot disagree about what a row says.
func WellOf(t *gridwellv1.Tile) Well {
	return Well{
		ID: t.Id, X: t.X, Y: t.Y, W: t.W, H: t.H,
		ViewCx: t.ViewCx, ViewCy: t.ViewCy, ViewZoom: t.ViewZoom,
	}
}

// PreviewFactor is the scale at which a well shows its child grid: one child
// cell is parent_cell_size / PreviewFactor pixels.
const PreviewFactor = 8.0

// DefaultWellViewZoom stands in for a never-visited well's ViewZoom. It is
// picked so LiveFromIntrinsic yields the PreviewFactor calibration unchanged.
const DefaultWellViewZoom = 1.0 / PreviewFactor

// EffectiveViewZoom returns stored if positive, else fallback: the one place
// the unvisited branch lives. A text tile passes its own fallback, the initial
// reading scale being pane-dependent.
func EffectiveViewZoom(stored, fallback float64) float64 {
	if stored > 0 {
		return stored
	}
	return fallback
}

// EffectiveCenter returns the child-grid point a doorway's framing centers
// on: the stored center, or the footprint's own center when unvisited, since
// the stored zeros would frame the child grid's top-left corner. It reads the
// same ViewZoom 0 sentinel as EffectiveViewZoom.
func EffectiveCenter(w Well) (cx, cy float64) {
	if w.ViewZoom > 0 {
		return w.ViewCx, w.ViewCy
	}
	return float64(w.W) / 2, float64(w.H) / 2
}

// WheelZoom zooms about the cursor, keeping the world point under it fixed.
// The step is capped at ±0.5 so a fast scroll covers more range without
// jumping, and a clamped zoom leaves the center alone, so there is no drift at
// the limits.
func WheelZoom(deltaY, oldZoom, cx, cy, cellX, cellY, factorBase, zMin, zMax float64) (zoom, newCx, newCy float64) {
	step := min(max(deltaY/200.0, -0.5), 0.5)
	z := min(max(oldZoom*math.Pow(factorBase, -step*4), zMin), zMax)
	if z == oldZoom {
		return z, cx, cy
	}
	ratio := oldZoom / z
	return z, cellX - (cellX-cx)*ratio, cellY - (cellY-cy)*ratio
}

// WellWheelView advances a hover-wheel zoom of a well's stored preview
// framing by one notch, anchored on the cursor. The center stays float all
// the way to the store, so a wheel burst's sub-cell drift survives the save.
// Pass the stored center for the first notch; there is no sentinel. changed
// is false when nothing moved, so a no-op wheel never mutates.
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

// Overtake is the zoom at which the footprint exceeds both dimensions of the
// reference rect. Wells use it, their descent target being to fill the pane;
// text tiles use Fit. Returns 1 on degenerate input.
func Overtake(footprintW, footprintH int64, refW, refH, cellPx float64) float64 {
	if footprintW <= 0 || footprintH <= 0 || cellPx <= 0 {
		return 1
	}
	zw := refW / (float64(footprintW) * cellPx)
	zh := refH / (float64(footprintH) * cellPx)
	return math.Max(zw, zh)
}

// Fit is the zoom at which the footprint exactly fits inside the reference
// rect. Text tiles use it, so the saved ViewZoom is calibrated against
// whichever dimension was binding the user's editing. Returns 1 on degenerate
// input.
func Fit(footprintW, footprintH int64, refW, refH, cellPx float64) float64 {
	if footprintW <= 0 || footprintH <= 0 || cellPx <= 0 {
		return 1
	}
	zw := refW / (float64(footprintW) * cellPx)
	zh := refH / (float64(footprintH) * cellPx)
	return math.Min(zw, zh)
}

// OvertakeZoom is Overtake for a Well. Text tiles call Overtake directly with
// the inner-box dimensions.
func OvertakeZoom(w Well, paneW, paneH, cellPx float64) float64 {
	return Overtake(w.W, w.H, paneW, paneH, cellPx)
}

// LiveFromIntrinsic reconstructs a live zoom from an intrinsic ratio and the
// current overtake. It and IntrinsicFromLive own the stored-ratio to live-zoom
// mapping. Returns 0 when viewZoom is 0, so a caller can detect never-visited.
func LiveFromIntrinsic(viewZoom, overtake float64) float64 {
	return viewZoom * overtake
}

// IntrinsicFromLive is LiveFromIntrinsic's inverse. Returns 0 on degenerate
// input so the caller can leave ViewZoom unset.
func IntrinsicFromLive(liveZoom, overtake float64) float64 {
	if overtake <= 0 || liveZoom <= 0 {
		return 0
	}
	return liveZoom / overtake
}

// Descent computes the three endpoints of a descent through w: mid pans and
// zooms to the well at Overtake, swap is the child-grid state right after the
// path swap, final eases out to the well's saved ratio. final is returned
// rather than reconstructed at the call site, so the ratio-to-zoom formula
// stays in one place.
func Descent(from Endpoints, w Well, paneW, paneH, cellPx float64) (mid, swap, final Endpoints) {
	wellCx := float64(w.X) + float64(w.W)/2
	wellCy := float64(w.Y) + float64(w.H)/2
	overtake := OvertakeZoom(w, paneW, paneH, cellPx)
	// The descent always zooms in, even from past the overtake zoom.
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
	ratio := EffectiveViewZoom(w.ViewZoom, DefaultWellViewZoom)
	swapZoom := LiveFromIntrinsic(ratio, zPTarget)
	swapCx, swapCy := EffectiveCenter(w)
	swap = Endpoints{
		Path: childPath,
		Cx:   swapCx,
		Cy:   swapCy,
		Zoom: swapZoom,
	}
	// final reconstructs from the real overtake, not zPTarget, so a descent
	// that started past Overtake eases out and every other one is a no-op.
	final = swap
	final.Zoom = LiveFromIntrinsic(ratio, overtake)
	return
}

// StoredView is the live framing a well's persisted view describes, the same
// numbers Descent's final lands on. Boot and the no-session-state ascent
// fallbacks read it, so a reload never lands on a framing the user did not set.
func StoredView(w Well, paneW, paneH, cellPx float64) (cx, cy, zoom float64) {
	ratio := EffectiveViewZoom(w.ViewZoom, DefaultWellViewZoom)
	cx, cy = EffectiveCenter(w)
	return cx, cy, LiveFromIntrinsic(ratio, OvertakeZoom(w, paneW, paneH, cellPx))
}

// ShownWellFraming is the framing a doorway row is already showing: the stored
// one, or what StoredView puts in its place when the row is the never-visited
// sentinel. The framing writeback diffs against this rather than against the
// zero row, which is not a framing at all, so a grid the user only looked at
// is never stamped with one.
func ShownWellFraming(w Well) rpc.Framing {
	cx, cy := EffectiveCenter(w)
	return rpc.Framing{Cx: cx, Cy: cy, Zoom: EffectiveViewZoom(w.ViewZoom, DefaultWellViewZoom)}
}

// ShownRootFraming is ShownWellFraming for a root grid, which no doorway leads
// into: an unvisited root sits at the grid origin at live zoom 1, not at
// DefaultWellViewZoom's preview calibration, so its intrinsic zoom is one over
// the synthetic 1×1 overtake and depends on the pane it is read in.
func ShownRootFraming(stored rpc.Framing, overtake float64) rpc.Framing {
	if stored.Zoom > 0 {
		return stored
	}
	return rpc.Framing{Zoom: IntrinsicFromLive(1, overtake)}
}

// Ascent computes the endpoints of an ascent back through w: from to mid in
// child coords, then a swap to to in parent coords. Same calibration as
// Descent, reversed.
func Ascent(from Endpoints, w Well, parentPath []string, paneW, paneH, cellPx float64) (mid, to Endpoints) {
	zPTarget := OvertakeZoom(w, paneW, paneH, cellPx)
	ratio := EffectiveViewZoom(w.ViewZoom, DefaultWellViewZoom)
	midZoom := LiveFromIntrinsic(ratio, zPTarget)
	midCx, midCy := EffectiveCenter(w)
	mid = Endpoints{
		Path: from.Path,
		Cx:   midCx,
		Cy:   midCy,
		Zoom: midZoom,
	}
	// The ascent always zooms out from the user's current state.
	if mid.Zoom > from.Zoom {
		mid.Zoom = from.Zoom
	}
	// The parent zoom after the swap is independent of ViewZoom, since the
	// preview cell size is cellPx × ViewZoom whatever the parent zoom.
	to = Endpoints{
		Path: parentPath,
		Cx:   float64(w.X) + float64(w.W)/2,
		Cy:   float64(w.Y) + float64(w.H)/2,
		Zoom: zPTarget,
	}
	return
}

// PanDist is a cell-unit delta as screen pixels, for the animation timing.
func PanDist(dx, dy, zoom, cellPx float64) float64 {
	return math.Hypot(dx, dy) * cellPx * zoom
}

// ZoomDist is log-zoom distance in PanDist's units, so the two add into one
// animation duration. factor weights zoom against pixels; the renderer uses 4
// so zoom phases take the bulk of the time.
func ZoomDist(z1, z2, cellPx, factor float64) float64 {
	if z1 <= 0 || z2 <= 0 {
		return 0
	}
	return math.Abs(math.Log(z2/z1)) * cellPx * factor
}
