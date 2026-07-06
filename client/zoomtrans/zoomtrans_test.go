package zoomtrans

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

const (
	cellPx        = 64.0
	standardPaneW = 1920.0
	standardPaneH = 1080.0
)

func TestOvertakeZoomTakesLargerOfDimRatios(t *testing.T) {
	// 1x1 well, 1920x1080 pane → must zoom 1920/64=30 to overtake width.
	z := OvertakeZoom(Well{W: 1, H: 1}, standardPaneW, standardPaneH, cellPx)
	if !near(z, 30) {
		t.Errorf("z = %v, want 30", z)
	}
	// 1x1 well, 1080x1920 pane → must zoom 1920/64 to overtake height.
	z = OvertakeZoom(Well{W: 1, H: 1}, standardPaneH, standardPaneW, cellPx)
	if !near(z, 30) {
		t.Errorf("portrait z = %v, want 30", z)
	}
	// 3x2 well, 1920x1080 pane → max(1920/192, 1080/128) = max(10, 8.4375).
	z = OvertakeZoom(Well{W: 3, H: 2}, standardPaneW, standardPaneH, cellPx)
	if !near(z, 10) {
		t.Errorf("z = %v, want 10", z)
	}
}

func TestOvertakeZoomGuards(t *testing.T) {
	if OvertakeZoom(Well{W: 0, H: 1}, 100, 100, cellPx) != 1 {
		t.Error("zero w should return 1")
	}
	if OvertakeZoom(Well{W: 1, H: 1}, 100, 100, 0) != 1 {
		t.Error("zero cellPx should return 1")
	}
}

func TestDescentMidIsOvertakeAndContinuity(t *testing.T) {
	from := Endpoints{Path: nil, Cx: 0, Cy: 0, Zoom: 1.0}
	w := Well{ID: "7", X: 5, Y: 3, W: 1, H: 1, ViewX: 0, ViewY: 0}
	mid, swap, final := Descent(from, w, standardPaneW, standardPaneH, cellPx)

	// Mid centers on well center; zoom is the overtake zoom (30 for the
	// 1x1 / 1920 case).
	if !near(mid.Cx, 5.5) || !near(mid.Cy, 3.5) {
		t.Errorf("mid center = (%v, %v)", mid.Cx, mid.Cy)
	}
	if !near(mid.Zoom, 30) {
		t.Errorf("mid zoom = %v, want 30", mid.Zoom)
	}
	// Swap has new path with well appended; viewport on well's view
	// region; zoom = mid.Zoom / PreviewFactor (legacy calibration check
	// for the unvisited fallback).
	if len(swap.Path) != 1 || swap.Path[0] != "7" {
		t.Errorf("swap path = %v", swap.Path)
	}
	if !near(swap.Cx, 0.5) || !near(swap.Cy, 0.5) {
		t.Errorf("swap center = (%v, %v)", swap.Cx, swap.Cy)
	}
	if !near(swap.Zoom, 30/PreviewFactor) {
		t.Errorf("swap zoom = %v, want %v", swap.Zoom, 30/PreviewFactor)
	}
	// For from.Zoom <= Overtake the final equals swap (no segment C).
	if !near(final.Zoom, swap.Zoom) {
		t.Errorf("final zoom = %v, want %v", final.Zoom, swap.Zoom)
	}
}

func TestDescentFinalReconstructsLiveZoom(t *testing.T) {
	// The bug the round-trip fix addressed: startDescent was computing
	// final.Zoom = ViewZoom literally, instead of ViewZoom × Overtake.
	// Now Descent returns final itself so the caller can't get it wrong.
	// Property: final.Zoom = ViewZoom × Overtake, for any from.Zoom
	// (including past Overtake — final still reconstructs the saved live
	// zoom, while swap.Zoom may differ for continuity).
	w := Well{ID: "1", W: 3, H: 2, ViewZoom: 0.671}
	overtake := OvertakeZoom(w, standardPaneW, standardPaneH, cellPx)
	wantLive := 0.671 * overtake
	for _, fromZoom := range []float64{0.5, 1.0, overtake, overtake * 2, 100} {
		from := Endpoints{Zoom: fromZoom}
		_, _, final := Descent(from, w, standardPaneW, standardPaneH, cellPx)
		if !near(final.Zoom, wantLive) {
			t.Errorf("from.Zoom=%v: final.Zoom=%v, want %v", fromZoom, final.Zoom, wantLive)
		}
	}
}

func TestDescentNeverZoomsOut(t *testing.T) {
	// Caller already zoomed in past the overtake zoom: descent must not
	// regress. mid.Zoom should be at least from.Zoom.
	from := Endpoints{Zoom: 50}
	w := Well{W: 1, H: 1}
	mid, _, _ := Descent(from, w, standardPaneW, standardPaneH, cellPx)
	if mid.Zoom < from.Zoom {
		t.Errorf("mid.Zoom = %v, want >= %v", mid.Zoom, from.Zoom)
	}
}

func TestDescentDoesNotShareSlice(t *testing.T) {
	from := Endpoints{Path: []string{"1", "2", "3"}, Zoom: 1}
	w := Well{ID: "9"}
	_, swap, _ := Descent(from, w, 100, 100, cellPx)
	swap.Path[0] = "999"
	if from.Path[0] == "999" {
		t.Error("Descent shared the path slice")
	}
}

func TestAscentNeverZoomsIn(t *testing.T) {
	// Caller is already at a tiny zoom; ascent must not zoom in.
	from := Endpoints{Path: []string{"42"}, Zoom: 0.5}
	w := Well{ID: "42", W: 1, H: 1, ViewX: 1, ViewY: 1}
	mid, _ := Ascent(from, w, nil, standardPaneW, standardPaneH, cellPx)
	if mid.Zoom > from.Zoom {
		t.Errorf("mid.Zoom = %v, want <= %v", mid.Zoom, from.Zoom)
	}
}

func TestAscentSwitchContinuity(t *testing.T) {
	// At the switch: child cell = cellPx * mid.Zoom; preview cell =
	// cellPx * to.Zoom / PreviewFactor. Equal => to.Zoom = mid.Zoom *
	// PreviewFactor.
	from := Endpoints{Path: []string{"42"}, Zoom: 5.0}
	w := Well{ID: "42", X: 1, Y: 2, W: 2, H: 1, ViewX: 0, ViewY: 0}
	mid, to := Ascent(from, w, nil, standardPaneW, standardPaneH, cellPx)
	if !near(to.Zoom, mid.Zoom*PreviewFactor) {
		t.Errorf("to.Zoom = %v, mid.Zoom*PreviewFactor = %v", to.Zoom, mid.Zoom*PreviewFactor)
	}
	// And the parent's viewport is centered on the well rect's center.
	if !near(to.Cx, 2) || !near(to.Cy, 2.5) {
		t.Errorf("to center = (%v, %v); want (2, 2.5)", to.Cx, to.Cy)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Intrinsic-ratio helpers.
//
// The bug that motivated this test suite: startDescent forgot to
// multiply ViewZoom by Overtake when reconstructing the live zoom, so
// every ascend→descend round trip shrank the content by a factor of
// Overtake. The tests below would have failed before that fix.

func TestLiveIntrinsicAreInverses(t *testing.T) {
	// LiveFromIntrinsic and IntrinsicFromLive must be exact inverses
	// for any non-degenerate input. This is the single property that
	// the whole intrinsic-ratio model relies on.
	cases := []struct{ live, overtake float64 }{
		{1.0, 1.0},
		{2.98, 4.44}, // values from the original bug repro
		{0.5, 10.0},
		{50.0, 0.1},
		{1e-3, 1e3},
		{1e3, 1e-3},
	}
	for _, c := range cases {
		vz := IntrinsicFromLive(c.live, c.overtake)
		got := LiveFromIntrinsic(vz, c.overtake)
		if !near(got, c.live) {
			t.Errorf("live=%v overtake=%v: round trip got %v", c.live, c.overtake, got)
		}
	}
}

func TestIntrinsicFromLiveGuards(t *testing.T) {
	if IntrinsicFromLive(2.0, 0) != 0 {
		t.Error("zero overtake should yield 0 ratio")
	}
	if IntrinsicFromLive(0, 4.0) != 0 {
		t.Error("zero liveZoom should yield 0 ratio")
	}
	if IntrinsicFromLive(2.0, -1.0) != 0 {
		t.Error("negative overtake should yield 0 ratio")
	}
}

func TestOvertakeEquivalentWellAndDirect(t *testing.T) {
	// The well-flavored OvertakeZoom convenience must produce the same
	// number as a direct Overtake call.
	w := Well{W: 3, H: 5}
	if !near(OvertakeZoom(w, standardPaneW, standardPaneH, cellPx),
		Overtake(3, 5, standardPaneW, standardPaneH, cellPx)) {
		t.Error("OvertakeZoom and Overtake disagree")
	}
}

func TestOvertakeFillsAtLeastOneDim(t *testing.T) {
	// At zoom = Overtake, the footprint's screen size in at least one
	// dimension equals the reference rect (the other overflows). This
	// is the geometric meaning of "max of dim ratios".
	for _, c := range []struct {
		fw, fh int64
		rw, rh float64
	}{
		{1, 1, standardPaneW, standardPaneH},
		{3, 2, standardPaneW, standardPaneH},
		{5, 5, 800, 600},
		{1, 10, 500, 500},
	} {
		z := Overtake(c.fw, c.fh, c.rw, c.rh, cellPx)
		footW := float64(c.fw) * cellPx * z
		footH := float64(c.fh) * cellPx * z
		// One dimension equals the ref rect; the other ≥.
		fillsW := near(footW, c.rw) && footH >= c.rh-1e-9
		fillsH := near(footH, c.rh) && footW >= c.rw-1e-9
		if !fillsW && !fillsH {
			t.Errorf("foot=(%v,%v) rect=(%v,%v): no dim filled; footprint=(%v,%v)",
				c.fw, c.fh, c.rw, c.rh, footW, footH)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────
// Round-trip ascend→descend identity.
//
// This is the property that the user's bug violated. The scenario:
// user is in a well at live zoom L0; ascends (we save ViewZoom); then
// descends again into the same well; the reconstructed live zoom must
// equal L0. Tested across pane sizes — preview/live state must be
// stable across window resizes between ascent and descent.

func TestWellRoundTripSamePane(t *testing.T) {
	// Same pane size at ascent and descent: live zoom must be restored
	// exactly. Iterates a range of starting zooms to cover the case
	// the bug fix landed on (`final.Zoom = ViewZoom × Overtake`) and
	// edge cases (very small / very large live zoom).
	for _, L0 := range []float64{0.5, 1.0, 2.98, 10.0, 50.0} {
		w := Well{ID: "1", W: 3, H: 2}

		// Ascend: save the intrinsic ratio that lives on the well.
		overtake := OvertakeZoom(w, standardPaneW, standardPaneH, cellPx)
		w.ViewZoom = IntrinsicFromLive(L0, overtake)

		// Descend in the same pane: the reconstructed live zoom must
		// equal L0. (The actual restored zoom in the application is
		// computed as ViewZoom × overtake_now; this test guards that
		// formula directly, independent of the Descent endpoints.)
		got := LiveFromIntrinsic(w.ViewZoom, OvertakeZoom(w, standardPaneW, standardPaneH, cellPx))
		if !near(got, L0) {
			t.Errorf("L0=%v: round trip got %v", L0, got)
		}
	}
}

func TestWellRoundTripAcrossPaneResize(t *testing.T) {
	// Different pane size at descent than gridwell: live zoom necessarily
	// changes (the well's footprint is a different number of pixels
	// now), but the **visible child cells across the well width**
	// must be invariant. That's the pane-independent property the
	// intrinsic-ratio model exists to guarantee.
	for _, L0 := range []float64{1.0, 2.98, 7.5} {
		w := Well{ID: "1", W: 3, H: 2}
		// Ascent in pane A.
		ot1 := OvertakeZoom(w, standardPaneW, standardPaneH, cellPx)
		w.ViewZoom = IntrinsicFromLive(L0, ot1)
		// Visible child cells across the well width at gridwell:
		// well footprint screen px / cellPx_screen = (W × cellPx × overtake) / (cellPx × live)
		//                                          = W × overtake / live
		// With live = vz × overtake, this is W / vz.
		visibleA := float64(w.W) / w.ViewZoom

		// Descent in pane B (different size).
		ot2 := OvertakeZoom(w, 800, 1200, cellPx)
		L1 := LiveFromIntrinsic(w.ViewZoom, ot2)
		visibleB := float64(w.W) * ot2 / L1
		if !near(visibleA, visibleB) {
			t.Errorf("L0=%v: visible cells %v ≠ %v across pane resize",
				L0, visibleA, visibleB)
		}
	}
}

func TestPathSwapContinuityForIntrinsicRatio(t *testing.T) {
	// At the path swap, the just-before previewCell (parent grid view
	// of the well's contents) must equal the just-after liveCell
	// (child grid native render). For ViewZoom > 0 the formulae are:
	//   previewCell_just_before = cellPx × parentZoom × ViewZoom
	//                            = cellPx × Overtake × ViewZoom
	//   liveCell_just_after     = cellPx × childZoom
	//                            = cellPx × LiveFromIntrinsic(ViewZoom, Overtake)
	// These must be equal — that's what makes the descent feel
	// continuous. The old TestDescentMidIsOvertakeAndContinuity tests
	// the ViewZoom == 0 path (PreviewFactor calibration); this one
	// tests the populated-ratio path.
	from := Endpoints{Zoom: 1}
	for _, vz := range []float64{0.1, 0.25, 0.671, 1.0, 3.0} {
		w := Well{ID: "1", W: 3, H: 2, ViewZoom: vz}
		overtake := OvertakeZoom(w, standardPaneW, standardPaneH, cellPx)
		_, swap, _ := Descent(from, w, standardPaneW, standardPaneH, cellPx)
		// Just-before-swap parent zoom is the mid (= overtake when
		// from.Zoom <= overtake, which holds for from.Zoom = 1 here).
		previewCellPx := cellPx * overtake * vz
		liveCellPx := cellPx * swap.Zoom
		if !near(previewCellPx, liveCellPx) {
			t.Errorf("vz=%v: preview=%v live=%v", vz, previewCellPx, liveCellPx)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────
// File-side round-trip and continuity.
//
// Files use the same intrinsic-ratio model as wells; only the reference
// rectangle differs (inner-box vs full pane). The tests below pin the
// invariants at the math level so a future regression in fileLiveZoom
// / fileEffectiveRatio (in the wasm package, not testable directly)
// is caught by failures here when someone reaches for a shortcut.

func TestFileRoundTripSamePane(t *testing.T) {
	// Mirror of TestWellRoundTripSamePane: save & reconstruct a live
	// TextZoom across the same pane. Uses Fit (not Overtake) because
	// file fileOvertakeZoom calibrates against the *smaller* inner-box
	// dim — see Fit's docstring.
	innerW, innerH := 1760.0, 920.0
	fileOvertake := Fit(4, 3, innerW, innerH, cellPx)
	for _, L0 := range []float64{0.5, 1.0, 1.4, 3.0} {
		stored := IntrinsicFromLive(L0, fileOvertake)
		got := LiveFromIntrinsic(stored, fileOvertake)
		if !near(got, L0) {
			t.Errorf("L0=%v: got %v", L0, got)
		}
	}
}

func TestFileRoundTripAcrossPaneResize(t *testing.T) {
	// Saving at pane A and reconstructing at pane B preserves the
	// ratio. Uses Fit for the file overtake.
	for _, L0 := range []float64{1.0, 1.4} {
		innerAW, innerAH := 1760.0, 920.0
		ovA := Fit(4, 3, innerAW, innerAH, cellPx)
		stored := IntrinsicFromLive(L0, ovA)
		innerBW, innerBH := 1120.0, 560.0
		ovB := Fit(4, 3, innerBW, innerBH, cellPx)
		L1 := LiveFromIntrinsic(stored, ovB)
		if !near(stored, IntrinsicFromLive(L1, ovB)) {
			t.Errorf("L0=%v: ratio drift across pane resize", L0)
		}
	}
}

// TestFilePreviewMatchesLiveOnAspectMismatch is the test that would
// have caught the s6/s7 bug. Setup: a 1×1 file in a landscape pane
// (innerW > innerH). The invariant under test: in the preview, an
// h1 must fill the file cell's height at the SAME fraction it fills
// the inner-box height in live. That's "things stay where you put
// them, including relative sizes" applied to the file/cell pair —
// for whichever inner-box dimension the user's content fills.
//
// Files with aspect ≠ inner-box aspect can't preserve both width and
// height ratios simultaneously through the live→preview transform.
// Calibrating against the *smaller* inner-box dim (Fit, not Overtake)
// preserves height ratio in the landscape-pane case below — which is
// the dim that actually bounds the user's content (a title fills the
// available vertical room before it overflows the wider horizontal
// room). Width may overflow the cell on the right; the user accepts
// that ("starting from the left").
//
// Before switching fileOvertakeZoom from Overtake (max) to Fit (min),
// this test failed.
func TestFilePreviewMatchesLiveOnAspectMismatch(t *testing.T) {
	innerW, innerH := 2400.0, 1204.0
	fileW, fileH := int64(1), int64(1)
	const h1Px = 24.0 // logical px for h1 in markdown style

	// User has TextZoom that gives a readable h1 — value doesn't
	// matter as long as we measure fill fractions from it consistently.
	const liveFileZoom = 23.0
	liveHeightFill := liveFileZoom * h1Px / innerH

	// Save the intrinsic ratio via the file overtake.
	fileOvertake := Fit(fileW, fileH, innerW, innerH, cellPx)
	stored := IntrinsicFromLive(liveFileZoom, fileOvertake)

	// At any preview parent zoom, the rendered h1's fraction-of-cell-
	// height must equal the live fraction-of-innerH.
	for _, parentZoom := range []float64{0.5, 1.0, 2.5, 5.0} {
		previewScale := LiveFromIntrinsic(stored, parentZoom)
		previewH1Height := previewScale * h1Px
		cellHeight := cellPx * parentZoom
		previewHeightFill := previewH1Height / cellHeight

		if !near(previewHeightFill, liveHeightFill) {
			t.Errorf("parentZoom=%v: preview height fill = %v, want %v (live ratio)",
				parentZoom, previewHeightFill, liveHeightFill)
		}
	}
}

func TestFileFallbackUnifiesPreviewAndLive(t *testing.T) {
	// The file-side fallback rule (wasm-side helper fileEffectiveRatio)
	// is: substitute IntrinsicFromLive(fileInitialZoom, fileOvertake)
	// when stored is 0. Property under test: at the moment of descent
	// (parent zoom = fileOvertake), the preview scale and the live
	// TextZoom must agree — that's the path-swap continuity for
	// unvisited files, fixed in Phase 3.
	//
	// Math: preview_at_overtake = overtake × ratio
	//       live_first_descent = overtake × ratio  (when ratio =
	//         IntrinsicFromLive(fileInitialZoom, overtake))
	//                          = fileInitialZoom
	// So preview at descent should literally equal fileInitialZoom.
	for _, initialZoom := range []float64{0.5, 1.0, 1.4} {
		for _, overtake := range []float64{2.5, 5.0, 6.875, 10.0} {
			ratio := IntrinsicFromLive(initialZoom, overtake)
			previewAtOvertake := overtake * ratio
			if !near(previewAtOvertake, initialZoom) {
				t.Errorf("initial=%v overtake=%v: preview-at-swap=%v ≠ initial",
					initialZoom, overtake, previewAtOvertake)
			}
		}
	}
}

func TestAscentMidContinuityForIntrinsicRatio(t *testing.T) {
	// Mirror of TestPathSwapContinuityForIntrinsicRatio for gridwell:
	// at the switch, the just-before child cell equals the just-after
	// preview cell. Equivalent: mid.Zoom = ViewZoom × overtake.
	for _, vz := range []float64{0.25, 0.671, 1.0, 3.0} {
		w := Well{ID: "1", W: 3, H: 2, ViewZoom: vz}
		overtake := OvertakeZoom(w, standardPaneW, standardPaneH, cellPx)
		from := Endpoints{Path: []string{"1"}, Zoom: vz * overtake}
		mid, _ := Ascent(from, w, nil, standardPaneW, standardPaneH, cellPx)
		want := vz * overtake
		// Note: Ascent caps mid.Zoom at from.Zoom, so we expect equality.
		if !near(mid.Zoom, want) {
			t.Errorf("vz=%v: mid.Zoom=%v want %v", vz, mid.Zoom, want)
		}
	}
}

func TestPanDist(t *testing.T) {
	// (3, 4) is a 3-4-5 triangle: hypot = 5. At cellPx=64, zoom=1.0,
	// distance should be 5*64*1 = 320.
	got := PanDist(3, 4, 1, 64)
	if !near(got, 320) {
		t.Errorf("3-4 at zoom 1: got %v, want 320", got)
	}
	// Same delta, doubled zoom: distance doubles.
	got = PanDist(3, 4, 2, 64)
	if !near(got, 640) {
		t.Errorf("3-4 at zoom 2: got %v, want 640", got)
	}
	// Zero delta.
	if got := PanDist(0, 0, 1.5, 64); !near(got, 0) {
		t.Errorf("(0,0): got %v, want 0", got)
	}
}

func TestZoomDist(t *testing.T) {
	// log(e/1) = 1; with factor=1, cellPx=1, distance=1.
	if got := ZoomDist(1, math.E, 1, 1); !near(got, 1) {
		t.Errorf("1→e at unit weight: got %v, want 1", got)
	}
	// Symmetric: doubling vs halving the zoom should produce the same
	// magnitude.
	a := ZoomDist(1, 2, 64, 4)
	b := ZoomDist(2, 1, 64, 4)
	if !near(a, b) {
		t.Errorf("symmetry: 1→2 = %v, 2→1 = %v", a, b)
	}
	// Same start and end: zero motion.
	if got := ZoomDist(1.5, 1.5, 64, 4); !near(got, 0) {
		t.Errorf("identity: got %v, want 0", got)
	}
	// Degenerate (non-positive) inputs: zero, not NaN/-Inf.
	if got := ZoomDist(0, 1, 64, 4); got != 0 {
		t.Errorf("z1=0: got %v, want 0", got)
	}
	if got := ZoomDist(1, 0, 64, 4); got != 0 {
		t.Errorf("z2=0: got %v, want 0", got)
	}
	if got := ZoomDist(-1, 1, 64, 4); got != 0 {
		t.Errorf("z1<0: got %v, want 0", got)
	}
}

func TestWheelZoom(t *testing.T) {
	const base, zmin, zmax = 1.1, 0.25, 8.0

	// Zoom in (deltaY<0) toward the cursor: zoom grows, center moves toward
	// the cursor world point.
	z, cx, cy := WheelZoom(-100, 1.0, 0, 0, 10, 10, base, zmin, zmax)
	if z <= 1.0 {
		t.Errorf("scroll up should zoom in: z=%v", z)
	}
	if !(cx > 0 && cx < 10) || !(cy > 0 && cy < 10) {
		t.Errorf("center should move toward cursor (0<c<10): cx=%v cy=%v", cx, cy)
	}

	// Zoom out (deltaY>0): zoom shrinks.
	z, _, _ = WheelZoom(100, 1.0, 0, 0, 10, 10, base, zmin, zmax)
	if z >= 1.0 {
		t.Errorf("scroll down should zoom out: z=%v", z)
	}

	// Step cap: a huge delta is capped at ±0.5 step, so the factor equals
	// base^(-0.5*4) = base^-2 regardless of how big the delta is.
	zCapped, _, _ := WheelZoom(1e9, 1.0, 0, 0, 0, 0, base, zmin, zmax)
	if !near(zCapped, math.Pow(base, -2)) {
		t.Errorf("step cap: z=%v want %v", zCapped, math.Pow(base, -2))
	}

	// Clamp at max: zoom can't exceed zmax and the center doesn't drift when
	// the clamp pins zoom unchanged.
	z, cx, cy = WheelZoom(-1e9, zmax, 3, 4, 10, 10, base, zmin, zmax)
	if z != zmax || cx != 3 || cy != 4 {
		t.Errorf("clamped at max: z=%v c=(%v,%v), want %v (3,4)", z, cx, cy, zmax)
	}
	// Clamp at min.
	z, _, _ = WheelZoom(1e9, zmin, 0, 0, 0, 0, base, zmin, zmax)
	if z != zmin {
		t.Errorf("clamped at min: z=%v want %v", z, zmin)
	}
}

func TestPortalWellRoundsAndFloors(t *testing.T) {
	// Rounds the float cell rect to integer cells.
	w := PortalWell(2.4, 3.6, 1.0, 1.0)
	if w.ID != "portal" {
		t.Errorf("ID = %q, want portal", w.ID)
	}
	if w.X != 2 || w.Y != 4 {
		t.Errorf("rounded pos = (%d,%d), want (2,4)", w.X, w.Y)
	}
	if w.W != 1 || w.H != 1 {
		t.Errorf("size = (%d,%d), want (1,1)", w.W, w.H)
	}
	// A sub-cell footprint floors to at least 1×1 so the well is never empty.
	if w := PortalWell(0, 0, 0.2, 0.0); w.W != 1 || w.H != 1 {
		t.Errorf("degenerate size = (%d,%d), want (1,1)", w.W, w.H)
	}
}

// TestViewOriginFromCenterRoundTrip: the descend→ascend quantization is
// idempotent — reconstructing the origin from the center it produces returns
// the same origin, for every origin and tile size. The old round(center)-w/2
// drifted +1 per round trip for odd sizes (the .5 fraction rounds away from
// zero); caught by preview-stability.spec.ts the day it could be observed.
func TestViewOriginFromCenterRoundTrip(t *testing.T) {
	for _, origin := range []int64{-3, -1, 0, 1, 2, 7, 100} {
		for _, size := range []int64{1, 2, 3, 5} {
			center := float64(origin) + float64(size)/2
			if got := ViewOriginFromCenter(center, size); got != origin {
				t.Errorf("origin %d size %d: center %v → %d, want the same origin", origin, size, center, got)
			}
		}
	}
	// A genuine reframe still quantizes to the nearest window.
	if got := ViewOriginFromCenter(4.9, 1); got != 4 {
		t.Errorf("center 4.9 size 1 → %d, want 4", got)
	}
	if got := ViewOriginFromCenter(-2.7, 2); got != -4 {
		t.Errorf("center -2.7 size 2 → %d, want -4", got)
	}
}
