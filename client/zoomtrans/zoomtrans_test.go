package zoomtrans

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

const cellPx = 64.0

func TestOvertakeZoomTakesLargerOfDimRatios(t *testing.T) {
	// 1x1 well, 1920x1080 pane → must zoom 1920/64=30 to overtake width.
	z := OvertakeZoom(Well{W: 1, H: 1}, 1920, 1080, cellPx)
	if !near(z, 30) {
		t.Errorf("z = %v, want 30", z)
	}
	// 1x1 well, 1080x1920 pane → must zoom 1920/64 to overtake height.
	z = OvertakeZoom(Well{W: 1, H: 1}, 1080, 1920, cellPx)
	if !near(z, 30) {
		t.Errorf("portrait z = %v, want 30", z)
	}
	// 3x2 well, 1920x1080 pane → max(1920/192, 1080/128) = max(10, 8.4375).
	z = OvertakeZoom(Well{W: 3, H: 2}, 1920, 1080, cellPx)
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
	w := Well{ID: 7, X: 5, Y: 3, W: 1, H: 1, ViewX: 0, ViewY: 0}
	mid, swap, final := Descent(from, w, 1920, 1080, cellPx)

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
	if len(swap.Path) != 1 || swap.Path[0] != 7 {
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
	w := Well{ID: 1, W: 3, H: 2, ViewZoom: 0.671}
	overtake := OvertakeZoom(w, 1920, 1080, cellPx)
	wantLive := 0.671 * overtake
	for _, fromZoom := range []float64{0.5, 1.0, overtake, overtake * 2, 100} {
		from := Endpoints{Zoom: fromZoom}
		_, _, final := Descent(from, w, 1920, 1080, cellPx)
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
	mid, _, _ := Descent(from, w, 1920, 1080, cellPx)
	if mid.Zoom < from.Zoom {
		t.Errorf("mid.Zoom = %v, want >= %v", mid.Zoom, from.Zoom)
	}
}

func TestDescentDoesNotShareSlice(t *testing.T) {
	from := Endpoints{Path: []int64{1, 2, 3}, Zoom: 1}
	w := Well{ID: 9}
	_, swap, _ := Descent(from, w, 100, 100, cellPx)
	swap.Path[0] = 999
	if from.Path[0] == 999 {
		t.Error("Descent shared the path slice")
	}
}

func TestAscentNeverZoomsIn(t *testing.T) {
	// Caller is already at a tiny zoom; ascent must not zoom in.
	from := Endpoints{Path: []int64{42}, Zoom: 0.5}
	w := Well{ID: 42, W: 1, H: 1, ViewX: 1, ViewY: 1}
	mid, _ := Ascent(from, w, nil, 1920, 1080, cellPx)
	if mid.Zoom > from.Zoom {
		t.Errorf("mid.Zoom = %v, want <= %v", mid.Zoom, from.Zoom)
	}
}

func TestAscentSwitchContinuity(t *testing.T) {
	// At the switch: child cell = cellPx * mid.Zoom; preview cell =
	// cellPx * to.Zoom / PreviewFactor. Equal => to.Zoom = mid.Zoom *
	// PreviewFactor.
	from := Endpoints{Path: []int64{42}, Zoom: 5.0}
	w := Well{ID: 42, X: 1, Y: 2, W: 2, H: 1, ViewX: 0, ViewY: 0}
	mid, to := Ascent(from, w, nil, 1920, 1080, cellPx)
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
	if !near(OvertakeZoom(w, 1920, 1080, cellPx),
		Overtake(3, 5, 1920, 1080, cellPx)) {
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
		{1, 1, 1920, 1080},
		{3, 2, 1920, 1080},
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
		paneW, paneH := 1920.0, 1080.0
		w := Well{ID: 1, W: 3, H: 2}

		// Ascend: save the intrinsic ratio that lives on the well.
		overtake := OvertakeZoom(w, paneW, paneH, cellPx)
		w.ViewZoom = IntrinsicFromLive(L0, overtake)

		// Descend in the same pane: the reconstructed live zoom must
		// equal L0. (The actual restored zoom in the application is
		// computed as ViewZoom × overtake_now; this test guards that
		// formula directly, independent of the Descent endpoints.)
		got := LiveFromIntrinsic(w.ViewZoom, OvertakeZoom(w, paneW, paneH, cellPx))
		if !near(got, L0) {
			t.Errorf("L0=%v: round trip got %v", L0, got)
		}
	}
}

func TestWellRoundTripAcrossPaneResize(t *testing.T) {
	// Different pane size at descent than ascent: live zoom necessarily
	// changes (the well's footprint is a different number of pixels
	// now), but the **visible child cells across the well width**
	// must be invariant. That's the pane-independent property the
	// intrinsic-ratio model exists to guarantee.
	for _, L0 := range []float64{1.0, 2.98, 7.5} {
		w := Well{ID: 1, W: 3, H: 2}
		// Ascent in pane A.
		ot1 := OvertakeZoom(w, 1920, 1080, cellPx)
		w.ViewZoom = IntrinsicFromLive(L0, ot1)
		// Visible child cells across the well width at ascent:
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
		w := Well{ID: 1, W: 3, H: 2, ViewZoom: vz}
		paneW, paneH := 1920.0, 1080.0
		overtake := OvertakeZoom(w, paneW, paneH, cellPx)
		_, swap, _ := Descent(from, w, paneW, paneH, cellPx)
		// Just-before-swap parent zoom is the mid (= overtake when
		// from.Zoom <= overtake, which holds for from.Zoom = 1 here).
		previewCellPx := cellPx * overtake * vz
		liveCellPx := cellPx * swap.Zoom
		if !near(previewCellPx, liveCellPx) {
			t.Errorf("vz=%v: preview=%v live=%v", vz, previewCellPx, liveCellPx)
		}
	}
}

func TestAscentMidContinuityForIntrinsicRatio(t *testing.T) {
	// Mirror of TestPathSwapContinuityForIntrinsicRatio for ascent:
	// at the switch, the just-before child cell equals the just-after
	// preview cell. Equivalent: mid.Zoom = ViewZoom × overtake.
	for _, vz := range []float64{0.25, 0.671, 1.0, 3.0} {
		w := Well{ID: 1, W: 3, H: 2, ViewZoom: vz}
		paneW, paneH := 1920.0, 1080.0
		overtake := OvertakeZoom(w, paneW, paneH, cellPx)
		from := Endpoints{Path: []int64{1}, Zoom: vz * overtake}
		mid, _ := Ascent(from, w, nil, paneW, paneH, cellPx)
		want := vz * overtake
		// Note: Ascent caps mid.Zoom at from.Zoom, so we expect equality.
		if !near(mid.Zoom, want) {
			t.Errorf("vz=%v: mid.Zoom=%v want %v", vz, mid.Zoom, want)
		}
	}
}
