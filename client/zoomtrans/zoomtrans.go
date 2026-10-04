// Package zoomtrans computes the (Cx, Cy, Zoom) endpoints of the zoom into and
// out of a well, calibrated so a child cell drawn as a preview and drawn
// natively are the same size at the path swap. Every computation measures
// against a Size, so a pane with no rect has no framing to compute.
package zoomtrans

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"math"
	"slices"

	"github.com/josephburnett/gridwell/api/rpc"
)

// Size is a pane's extent in pixels, finite and non-empty. SizeOf is its one
// constructor; a pane absent from the layout has none.
type Size struct{ w, h float64 }

// SizeOf refuses an extent that is not a finite area above zero.
func SizeOf(w, h float64) (Size, bool) {
	if !rpc.Finite(w) || !rpc.Finite(h) || w <= 0 || h <= 0 {
		return Size{}, false
	}
	return Size{w, h}, true
}

// Origin is the live view of a grid nobody has framed: its origin at zoom 1,
// where a fresh pane and a root never visited sit.
var Origin, _ = rpc.NewFraming(0, 0, 1)

// Endpoints is one end of a transition: descent path, viewport center in
// cells, zoom multiplier.
type Endpoints struct {
	Path   []string
	Cx, Cy float64
	Zoom   float64
}

// view is e's viewport as the checked value, so a transition that would run
// through a center or zoom past float range is refused rather than drawn.
func (e Endpoints) view() bool {
	_, err := rpc.NewFraming(e.Cx, e.Cy, e.Zoom)
	return err == nil
}

// Well is a doorway's footprint plus the framing it was left at, whose zoom
// is dimensionless (live scale over the overtake), so a descent reconstructs
// the live zoom for the current pane size.
type Well struct {
	ID   string
	X, Y int64
	W, H int64
	View rpc.View
}

// WellOf reads a tile row as a Well, the one derivation preview, descent and
// ascent share.
func WellOf(t *gridwellv1.Tile) Well {
	return Well{
		ID: t.Id, X: t.X, Y: t.Y, W: t.W, H: t.H,
		View: rpc.ViewOf(t.ViewCx, t.ViewCy, t.ViewZoom),
	}
}

// PreviewFactor is the scale at which a well shows its child grid: one child
// cell is parent_cell_size / PreviewFactor pixels.
const PreviewFactor = 8.0

// DefaultWellViewZoom stands in for a never-visited well's zoom. It is picked
// so LiveFromIntrinsic yields the PreviewFactor calibration unchanged.
const DefaultWellViewZoom = 1.0 / PreviewFactor

// Ratio is the intrinsic zoom a doorway shows its grid at: the saved one, or
// DefaultWellViewZoom when never visited. The one place that branch lives.
func (w Well) Ratio() float64 {
	if f, ok := w.View.Framing(); ok {
		return f.Zoom()
	}
	return DefaultWellViewZoom
}

// Center is the child-grid point a doorway's framing centers on: the saved
// center, or the footprint's own center when never visited.
func (w Well) Center() (cx, cy float64) {
	if f, ok := w.View.Framing(); ok {
		return f.Cx(), f.Cy()
	}
	return float64(w.W) / 2, float64(w.H) / 2
}

// WheelZoom zooms about the cursor, keeping the world point under it fixed.
// The step is capped at ±0.5, and a clamped zoom leaves the center alone. It
// refuses a view that is not one or a cursor that is not a point, so a wheel
// can never turn a center NaN.
func WheelZoom(deltaY, oldZoom, cx, cy, cellX, cellY, factorBase, zMin, zMax float64) (zoom, newCx, newCy float64, ok bool) {
	if _, err := rpc.NewFraming(cx, cy, oldZoom); err != nil || !rpc.Finite(cellX) || !rpc.Finite(cellY) || !rpc.Finite(deltaY) {
		return oldZoom, cx, cy, false
	}
	step := min(max(deltaY/200.0, -0.5), 0.5)
	z := min(max(oldZoom*math.Pow(factorBase, -step*4), zMin), zMax)
	if z == oldZoom {
		return z, cx, cy, true
	}
	ratio := oldZoom / z
	next, err := rpc.NewFraming(cellX-(cellX-cx)*ratio, cellY-(cellY-cy)*ratio, z)
	if err != nil {
		return oldZoom, cx, cy, false
	}
	return next.Zoom(), next.Cx(), next.Cy(), true
}

// WellWheelView advances a hover-wheel zoom of a well's stored preview
// framing by one notch, anchored on the cursor. The center stays float to the
// store so sub-cell drift survives. changed is false when nothing moved, so a
// no-op wheel never mutates.
func WellWheelView(deltaY float64, w Well, parentCell, cursorDxPx, cursorDyPx, cx0, cy0, factorBase, rMin, rMax float64) (cx1, cy1, ratio float64, changed bool) {
	r0 := w.Ratio()
	previewCell := parentCell * r0
	if !(previewCell > 0) {
		return cx0, cy0, r0, false
	}
	px := cx0 + cursorDxPx/previewCell
	py := cy0 + cursorDyPx/previewCell
	r1, c1x, c1y, ok := WheelZoom(deltaY, r0, cx0, cy0, px, py, factorBase, rMin, rMax)
	if !ok || r1 == r0 {
		return cx0, cy0, r0, false
	}
	return c1x, c1y, r1, true
}

// Overtake is the zoom at which the footprint exceeds both dimensions of s, a
// well's descent target. Returns 1 on a degenerate footprint or cell.
func Overtake(footprintW, footprintH int64, s Size, cellPx float64) float64 {
	if footprintW <= 0 || footprintH <= 0 || !(cellPx > 0) {
		return 1
	}
	zw := s.w / (float64(footprintW) * cellPx)
	zh := s.h / (float64(footprintH) * cellPx)
	return math.Max(zw, zh)
}

// Fit is the zoom at which the footprint exactly fits inside the reference
// rect, a text tile's calibration. Returns 1 on degenerate input.
func Fit(footprintW, footprintH int64, refW, refH, cellPx float64) float64 {
	if footprintW <= 0 || footprintH <= 0 || cellPx <= 0 {
		return 1
	}
	zw := refW / (float64(footprintW) * cellPx)
	zh := refH / (float64(footprintH) * cellPx)
	return math.Min(zw, zh)
}

// OvertakeZoom is Overtake for a Well.
func OvertakeZoom(w Well, s Size, cellPx float64) float64 {
	return Overtake(w.W, w.H, s, cellPx)
}

// LiveFromIntrinsic reconstructs a live zoom from an intrinsic ratio and the
// current overtake.
func LiveFromIntrinsic(viewZoom, overtake float64) float64 {
	return viewZoom * overtake
}

// IntrinsicFromLive is LiveFromIntrinsic's inverse. A degenerate input yields
// what rpc.NewFraming refuses, never a framing.
func IntrinsicFromLive(liveZoom, overtake float64) float64 {
	if overtake <= 0 || liveZoom <= 0 {
		return 0
	}
	return liveZoom / overtake
}

// Writeback is the framing a pane showing a doorway's grid at (cx, cy, live)
// writes onto the doorway's row, measured against foot's footprint at s, and
// whether it differs from shown, what the row already shows. A live view
// that is not one is no framing, so it writes nothing.
func Writeback(shown rpc.View, foot Well, cx, cy, live float64, s Size, cellPx float64) (rpc.Framing, bool) {
	next, err := rpc.NewFraming(cx, cy, IntrinsicFromLive(live, OvertakeZoom(foot, s, cellPx)))
	if err != nil || shown.SameAs(rpc.Saved(next)) {
		return rpc.Framing{}, false
	}
	return next, true
}

// Descent computes the three endpoints of a descent through w: mid pans and
// zooms to the well at Overtake, swap is the child-grid state right after the
// path swap, final eases out to the well's saved ratio. ok is false when an
// endpoint is not a view.
func Descent(from Endpoints, w Well, s Size, cellPx float64) (mid, swap, final Endpoints, ok bool) {
	wellCx := float64(w.X) + float64(w.W)/2
	wellCy := float64(w.Y) + float64(w.H)/2
	overtake := OvertakeZoom(w, s, cellPx)
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
	ratio := w.Ratio()
	swapZoom := LiveFromIntrinsic(ratio, zPTarget)
	swapCx, swapCy := w.Center()
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
	ok = mid.view() && swap.view() && final.view()
	return
}

// StoredView is the live view a well's persisted framing describes at s, the
// same numbers Descent's final lands on; false when that is not a view.
func StoredView(w Well, s Size, cellPx float64) (rpc.Framing, bool) {
	cx, cy := w.Center()
	f, err := rpc.NewFraming(cx, cy, LiveFromIntrinsic(w.Ratio(), OvertakeZoom(w, s, cellPx)))
	return f, err == nil
}

// ShownWellFraming is the framing a doorway row is already showing: the stored
// one, or StoredView's for a doorway never visited. The writeback diffs
// against this, so a grid the user only looked at is never stamped.
func ShownWellFraming(w Well) rpc.View {
	cx, cy := w.Center()
	return rpc.ViewOf(cx, cy, w.Ratio())
}

// ShownRootFraming is ShownWellFraming for a root grid, which no doorway leads
// into: an unvisited root sits at the grid origin at live zoom 1, not at
// DefaultWellViewZoom's preview calibration, so its intrinsic zoom is one over
// the synthetic 1×1 overtake and depends on the pane it is read in.
func ShownRootFraming(stored rpc.View, s Size, cellPx float64) rpc.View {
	if _, ok := stored.Framing(); ok {
		return stored
	}
	return rpc.ViewOf(Origin.Cx(), Origin.Cy(), IntrinsicFromLive(Origin.Zoom(), Overtake(1, 1, s, cellPx)))
}

// Ascent computes the endpoints of an ascent back through w, Descent's
// calibration reversed. ok is false when an endpoint is not a view.
func Ascent(from Endpoints, w Well, parentPath []string, s Size, cellPx float64) (mid, to Endpoints, ok bool) {
	zPTarget := OvertakeZoom(w, s, cellPx)
	midZoom := LiveFromIntrinsic(w.Ratio(), zPTarget)
	midCx, midCy := w.Center()
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
	// The parent zoom after the swap is independent of the ratio, since the
	// preview cell size is cellPx × ratio whatever the parent zoom.
	to = Endpoints{
		Path: parentPath,
		Cx:   float64(w.X) + float64(w.W)/2,
		Cy:   float64(w.Y) + float64(w.H)/2,
		Zoom: zPTarget,
	}
	ok = mid.view() && to.view()
	return
}

// PanDist is a cell-unit delta as screen pixels, for the animation timing.
func PanDist(dx, dy, zoom, cellPx float64) float64 {
	return math.Hypot(dx, dy) * cellPx * zoom
}

// ZoomDist is log-zoom distance in PanDist's units, so the two add into one
// animation duration; factor weights zoom against pixels.
func ZoomDist(z1, z2, cellPx, factor float64) float64 {
	if z1 <= 0 || z2 <= 0 {
		return 0
	}
	return math.Abs(math.Log(z2/z1)) * cellPx * factor
}
