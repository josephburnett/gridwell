package zoomtrans

import (
	"math"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
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
	w := Well{ID: "7", X: 5, Y: 3, W: 1, H: 1, ViewCx: 0.5, ViewCy: 0.5}
	mid, swap, final := Descent(from, w, standardPaneW, standardPaneH, cellPx)

	// Mid centers on well center; zoom is the overtake zoom (30 for the
	// 1x1 / 1920 case).
	if !near(mid.Cx, 5.5) || !near(mid.Cy, 3.5) {
		t.Errorf("mid center = (%v, %v)", mid.Cx, mid.Cy)
	}
	if !near(mid.Zoom, 30) {
		t.Errorf("mid zoom = %v, want 30", mid.Zoom)
	}
	// Swap zoom is mid.Zoom / PreviewFactor, the unvisited calibration.
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
	// final.Zoom is ViewZoom × Overtake for any from.Zoom, including past
	// Overtake, where swap.Zoom differs for continuity.
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
	// From past the overtake zoom, mid.Zoom is at least from.Zoom.
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
	w := Well{ID: "42", W: 1, H: 1, ViewCx: 1.5, ViewCy: 1.5}
	mid, _ := Ascent(from, w, nil, standardPaneW, standardPaneH, cellPx)
	if mid.Zoom > from.Zoom {
		t.Errorf("mid.Zoom = %v, want <= %v", mid.Zoom, from.Zoom)
	}
}

func TestAscentSwitchContinuity(t *testing.T) {
	// Child cell equals preview cell at the switch, so to.Zoom is
	// mid.Zoom * PreviewFactor.
	from := Endpoints{Path: []string{"42"}, Zoom: 5.0}
	w := Well{ID: "42", X: 1, Y: 2, W: 2, H: 1, ViewCx: 1, ViewCy: 0.5}
	mid, to := Ascent(from, w, nil, standardPaneW, standardPaneH, cellPx)
	if !near(to.Zoom, mid.Zoom*PreviewFactor) {
		t.Errorf("to.Zoom = %v, mid.Zoom*PreviewFactor = %v", to.Zoom, mid.Zoom*PreviewFactor)
	}
	if !near(to.Cx, 2) || !near(to.Cy, 2.5) {
		t.Errorf("to center = (%v, %v); want (2, 2.5)", to.Cx, to.Cy)
	}
}

// Intrinsic-ratio helpers. The live zoom is ViewZoom × Overtake;
// reconstructing it without the Overtake factor shrinks the content by that
// factor on every round trip.

func TestLiveIntrinsicAreInverses(t *testing.T) {
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
	w := Well{W: 3, H: 5}
	if !near(OvertakeZoom(w, standardPaneW, standardPaneH, cellPx),
		Overtake(3, 5, standardPaneW, standardPaneH, cellPx)) {
		t.Error("OvertakeZoom and Overtake disagree")
	}
}

func TestOvertakeFillsAtLeastOneDim(t *testing.T) {
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
		fillsW := near(footW, c.rw) && footH >= c.rh-1e-9
		fillsH := near(footH, c.rh) && footW >= c.rw-1e-9
		if !fillsW && !fillsH {
			t.Errorf("foot=(%v,%v) rect=(%v,%v): no dim filled; footprint=(%v,%v)",
				c.fw, c.fh, c.rw, c.rh, footW, footH)
		}
	}
}

// Ascend at live zoom L0, descend again, and the reconstructed live zoom is
// L0. Across pane sizes too: a window resize between ascent and descent must
// not move the framing.

func TestWellRoundTripSamePane(t *testing.T) {
	for _, L0 := range []float64{0.5, 1.0, 2.98, 10.0, 50.0} {
		w := Well{ID: "1", W: 3, H: 2}

		overtake := OvertakeZoom(w, standardPaneW, standardPaneH, cellPx)
		w.ViewZoom = IntrinsicFromLive(L0, overtake)

		// This guards the ViewZoom × overtake_now formula directly,
		// independent of the Descent endpoints.
		got := LiveFromIntrinsic(w.ViewZoom, OvertakeZoom(w, standardPaneW, standardPaneH, cellPx))
		if !near(got, L0) {
			t.Errorf("L0=%v: round trip got %v", L0, got)
		}
	}
}

func TestWellRoundTripAcrossPaneResize(t *testing.T) {
	// A different pane size changes the live zoom, but the visible child
	// cells across the well width stay invariant.
	for _, L0 := range []float64{1.0, 2.98, 7.5} {
		w := Well{ID: "1", W: 3, H: 2}
		ot1 := OvertakeZoom(w, standardPaneW, standardPaneH, cellPx)
		w.ViewZoom = IntrinsicFromLive(L0, ot1)
		// Visible child cells across the well width are W × overtake /
		// live, and with live = vz × overtake that is W / vz.
		visibleA := float64(w.W) / w.ViewZoom

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
	// The populated-ratio path of the swap continuity;
	// TestDescentMidIsOvertakeAndContinuity covers ViewZoom == 0.
	from := Endpoints{Zoom: 1}
	for _, vz := range []float64{0.1, 0.25, 0.671, 1.0, 3.0} {
		w := Well{ID: "1", W: 3, H: 2, ViewZoom: vz}
		overtake := OvertakeZoom(w, standardPaneW, standardPaneH, cellPx)
		_, swap, _ := Descent(from, w, standardPaneW, standardPaneH, cellPx)
		previewCellPx := cellPx * overtake * vz
		liveCellPx := cellPx * swap.Zoom
		if !near(previewCellPx, liveCellPx) {
			t.Errorf("vz=%v: preview=%v live=%v", vz, previewCellPx, liveCellPx)
		}
	}
}

// Files use the same intrinsic-ratio model as wells, with the inner box as
// the reference rect instead of the pane. These pin the invariants at the
// math level, because fileLiveZoom and fileEffectiveRatio live in the wasm
// package and are not testable directly.

func TestFileRoundTripSamePane(t *testing.T) {
	// Fit, not Overtake: see Fit.
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

// A tile whose aspect differs from the inner box cannot preserve both ratios
// through the live-to-preview transform. Fit calibrates against the smaller
// dimension, which is the one that bounds the user's content; width may
// overflow on the right, which the user reading from the left accepts.
func TestFilePreviewMatchesLiveOnAspectMismatch(t *testing.T) {
	innerW, innerH := 2400.0, 1204.0
	fileW, fileH := int64(1), int64(1)
	const h1Px = 24.0 // logical px for h1 in markdown style

	const liveFileZoom = 23.0
	liveHeightFill := liveFileZoom * h1Px / innerH

	fileOvertake := Fit(fileW, fileH, innerW, innerH, cellPx)
	stored := IntrinsicFromLive(liveFileZoom, fileOvertake)

	// At any preview parent zoom the h1's fraction of cell height equals
	// its live fraction of innerH.
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
	// Swap continuity for an unvisited file: with the wasm-side fallback
	// ratio IntrinsicFromLive(fileInitialZoom, overtake), the preview at
	// descent equals fileInitialZoom.
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
	// At the switch the child cell equals the preview cell, so mid.Zoom is
	// ViewZoom × overtake.
	for _, vz := range []float64{0.25, 0.671, 1.0, 3.0} {
		w := Well{ID: "1", W: 3, H: 2, ViewZoom: vz}
		overtake := OvertakeZoom(w, standardPaneW, standardPaneH, cellPx)
		from := Endpoints{Path: []string{"1"}, Zoom: vz * overtake}
		mid, _ := Ascent(from, w, nil, standardPaneW, standardPaneH, cellPx)
		want := vz * overtake
		if !near(mid.Zoom, want) {
			t.Errorf("vz=%v: mid.Zoom=%v want %v", vz, mid.Zoom, want)
		}
	}
}

func TestPanDist(t *testing.T) {
	got := PanDist(3, 4, 1, 64)
	if !near(got, 320) {
		t.Errorf("3-4 at zoom 1: got %v, want 320", got)
	}
	got = PanDist(3, 4, 2, 64)
	if !near(got, 640) {
		t.Errorf("3-4 at zoom 2: got %v, want 640", got)
	}
	if got := PanDist(0, 0, 1.5, 64); !near(got, 0) {
		t.Errorf("(0,0): got %v, want 0", got)
	}
}

func TestZoomDist(t *testing.T) {
	if got := ZoomDist(1, math.E, 1, 1); !near(got, 1) {
		t.Errorf("1→e at unit weight: got %v, want 1", got)
	}
	a := ZoomDist(1, 2, 64, 4)
	b := ZoomDist(2, 1, 64, 4)
	if !near(a, b) {
		t.Errorf("symmetry: 1→2 = %v, 2→1 = %v", a, b)
	}
	if got := ZoomDist(1.5, 1.5, 64, 4); !near(got, 0) {
		t.Errorf("identity: got %v, want 0", got)
	}
	// Degenerate inputs give zero, not NaN or -Inf.
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

	z, cx, cy := WheelZoom(-100, 1.0, 0, 0, 10, 10, base, zmin, zmax)
	if z <= 1.0 {
		t.Errorf("scroll up should zoom in: z=%v", z)
	}
	if !(cx > 0 && cx < 10) || !(cy > 0 && cy < 10) {
		t.Errorf("center should move toward cursor (0<c<10): cx=%v cy=%v", cx, cy)
	}

	z, _, _ = WheelZoom(100, 1.0, 0, 0, 10, 10, base, zmin, zmax)
	if z >= 1.0 {
		t.Errorf("scroll down should zoom out: z=%v", z)
	}

	// A huge delta caps at ±0.5 step, so the factor is base^-2.
	zCapped, _, _ := WheelZoom(1e9, 1.0, 0, 0, 0, 0, base, zmin, zmax)
	if !near(zCapped, math.Pow(base, -2)) {
		t.Errorf("step cap: z=%v want %v", zCapped, math.Pow(base, -2))
	}

	// A pinned zoom leaves the center where it was.
	z, cx, cy = WheelZoom(-1e9, zmax, 3, 4, 10, 10, base, zmin, zmax)
	if z != zmax || cx != 3 || cy != 4 {
		t.Errorf("clamped at max: z=%v c=(%v,%v), want %v (3,4)", z, cx, cy, zmax)
	}
	z, _, _ = WheelZoom(1e9, zmin, 0, 0, 0, 0, base, zmin, zmax)
	if z != zmin {
		t.Errorf("clamped at min: z=%v want %v", z, zmin)
	}
}

// A descent and an untouched ascent agree bit for bit at any center,
// sub-cell centers included. Quantizing the center to whole cells would drift
// a full cell per odd-sized round trip.
func TestFramingRoundTripIsByteIdentical(t *testing.T) {
	const paneW, paneH, cell = 1280, 800, 64
	for _, w := range []Well{
		{X: 2, Y: 3, W: 2, H: 2, ViewCx: 5.37, ViewCy: -7.125, ViewZoom: 0.4},
		{X: 0, Y: 0, W: 1, H: 1, ViewCx: 0.5, ViewCy: 0.5, ViewZoom: 1.0 / PreviewFactor},
		{X: 9, Y: 9, W: 3, H: 5, ViewCx: -0.0001, ViewCy: 1e6 + 0.5, ViewZoom: 0.9},
	} {
		cx, cy, live := StoredView(w, paneW, paneH, cell)
		gotZoom := IntrinsicFromLive(live, OvertakeZoom(w, paneW, paneH, cell))
		if cx != w.ViewCx || cy != w.ViewCy {
			t.Errorf("round trip moved the center: %+v → (%v, %v)", w, cx, cy)
		}
		// One multiply and one divide can return a single ulp off, far
		// inside the persister's no-op guard (rpc.Framing.SameAs, 1e-3),
		// so an untouched round trip writes nothing.
		if math.Abs(gotZoom-w.ViewZoom) > 1e-12 {
			t.Errorf("round trip changed the zoom: %v → %v", w.ViewZoom, gotZoom)
		}
	}
}

func TestWellWheelViewAnchorsAtCursor(t *testing.T) {
	w := Well{X: 0, Y: 0, W: 2, H: 2, ViewCx: 5, ViewCy: 7, ViewZoom: 0.25}
	const parentCell = 64.0
	cx0, cy0 := w.ViewCx, w.ViewCy

	// The anchor is the point under the cursor, so a cursor at the well
	// center moves nothing.
	cx1, cy1, r1, changed := WellWheelView(-120, w, parentCell, 0, 0, cx0, cy0, 1.1, 1.0/64, 1.0)
	if !changed || r1 <= 0.25 {
		t.Fatalf("wheel-in: ratio = %v changed=%v, want a larger ratio", r1, changed)
	}
	if cx1 != cx0 || cy1 != cy0 {
		t.Errorf("center-anchored zoom moved the center: (%v, %v) -> (%v, %v)", cx0, cy0, cx1, cy1)
	}

	// Float in, float out: no per-notch quantization eats the drift.
	const dx = 40.0
	cx1, cy1, r1, changed = WellWheelView(-120, w, parentCell, dx, 0, cx0, cy0, 1.1, 1.0/64, 1.0)
	if !changed {
		t.Fatal("off-center wheel-in: no change")
	}
	r0 := EffectiveViewZoom(w.ViewZoom, DefaultWellViewZoom)
	px := cx0 + dx/(parentCell*r0)
	got := (px - cx1) * parentCell * r1
	if math.Abs(got-dx) > 0.001 {
		t.Errorf("anchor drifted: cursor point now at %vpx from center, want exactly %v", got, dx)
	}
	if cx1 <= cx0 {
		t.Errorf("zooming toward a rightward cursor must drift the center right: %v -> %v", cx0, cx1)
	}
	if cy1 != cy0 {
		t.Errorf("no vertical cursor offset, but the center moved: %v -> %v", cy0, cy1)
	}

	// Each notch feeds the previous float center back in, so a burst keeps
	// moving; integer quantization would round every step back to the start.
	ww := w
	ccx, ccy := cx0, cy0
	for i := 0; i < 4; i++ {
		var r float64
		ccx, ccy, r, changed = WellWheelView(-120, ww, parentCell, dx, dx, ccx, ccy, 1.1, 1.0/64, 1.0)
		if !changed {
			t.Fatalf("notch %d: no change", i)
		}
		ww.ViewZoom = r
	}
	if ccx-cx0 < 0.3 || ccy-cy0 < 0.3 {
		t.Errorf("burst drift too small: center moved (%v, %v) cells; the drift must compound", ccx-cx0, ccy-cy0)
	}
}

// One conversion, two readers: a drift between StoredView and Descent's final
// would make coming back later land differently than descending now.
func TestStoredViewMatchesDescentFinal(t *testing.T) {
	wells := []Well{
		{X: 2, Y: 3, W: 2, H: 2, ViewCx: 6, ViewCy: 8, ViewZoom: 0.4},
		{X: 0, Y: 0, W: 1, H: 1},                // unvisited: default ratio
		{X: 1, Y: 1, W: 3, H: 1, ViewZoom: 1.0}, // max ratio
	}
	for _, w := range wells {
		from := Endpoints{Cx: 1, Cy: 1, Zoom: 1}
		_, _, final := Descent(from, w, 1280, 800, 64)
		cx, cy, zoom := StoredView(w, 1280, 800, 64)
		if cx != final.Cx || cy != final.Cy || zoom != final.Zoom {
			t.Errorf("StoredView(%+v) = (%v,%v,%v), Descent final = (%v,%v,%v)",
				w, cx, cy, zoom, final.Cx, final.Cy, final.Zoom)
		}
	}
}

// A never-visited doorway frames the middle of its child's origin cell.
// Reading ViewCx and ViewCy raw would slide every unvisited grid up and left
// by half a footprint.
func TestNeverVisitedFramingCentersTheFootprint(t *testing.T) {
	w := Well{ID: "1", X: 4, Y: 2, W: 3, H: 2}
	if cx, cy := EffectiveCenter(w); !near(cx, 1.5) || !near(cy, 1) {
		t.Errorf("EffectiveCenter = (%v, %v), want (1.5, 1)", cx, cy)
	}
	cx, cy, _ := StoredView(w, standardPaneW, standardPaneH, cellPx)
	if !near(cx, 1.5) || !near(cy, 1) {
		t.Errorf("StoredView center = (%v, %v), want (1.5, 1)", cx, cy)
	}
	_, swap, final := Descent(Endpoints{Zoom: 1}, w, standardPaneW, standardPaneH, cellPx)
	if !near(swap.Cx, 1.5) || !near(swap.Cy, 1) {
		t.Errorf("Descent swap center = (%v, %v), want (1.5, 1)", swap.Cx, swap.Cy)
	}
	if !near(final.Cx, 1.5) || !near(final.Cy, 1) {
		t.Errorf("Descent final center = (%v, %v), want (1.5, 1)", final.Cx, final.Cy)
	}
	mid, _ := Ascent(Endpoints{Path: []string{"1"}, Zoom: 100}, w, nil,
		standardPaneW, standardPaneH, cellPx)
	if !near(mid.Cx, 1.5) || !near(mid.Cy, 1) {
		t.Errorf("Ascent mid center = (%v, %v), want (1.5, 1)", mid.Cx, mid.Cy)
	}

	// A 1x1 synthetic doorway frames the middle of child cell (0,0).
	if cx, cy := EffectiveCenter(Well{W: 1, H: 1}); !near(cx, 0.5) || !near(cy, 0.5) {
		t.Errorf("root doorway center = (%v, %v), want (0.5, 0.5)", cx, cy)
	}

	// A visited doorway keeps a stored center of (0,0), which the sentinel
	// must not mistake for unvisited.
	v := Well{W: 3, H: 2, ViewZoom: 0.4}
	if cx, cy := EffectiveCenter(v); cx != 0 || cy != 0 {
		t.Errorf("visited center = (%v, %v), want (0, 0)", cx, cy)
	}
}

// The round trip above is byte-identical only because the row is a framing. A
// never-visited row is not one: its readers show a fallback in its place, so
// what a writeback must diff against is that fallback, never the zero row.
// Otherwise the first settle tick after a grid is merely looked at stamps a
// framing on it, and a root grid's stamp is derived from the pane it was
// looked at in.
func TestShowingAGridNeverStampsAFramingOnIt(t *testing.T) {
	const cell = 64.0
	for _, pane := range [][2]float64{{1280, 800}, {480, 900}} {
		paneW, paneH := pane[0], pane[1]
		for _, w := range []Well{
			{X: 0, Y: 0, W: 1, H: 1},
			{X: 4, Y: 2, W: 3, H: 2},
			{X: 2, Y: 3, W: 2, H: 2, ViewCx: 5.37, ViewCy: -7.125, ViewZoom: 0.4},
		} {
			cx, cy, live := StoredView(w, paneW, paneH, cell)
			saved := rpc.Framing{Cx: cx, Cy: cy,
				Zoom: IntrinsicFromLive(live, OvertakeZoom(w, paneW, paneH, cell))}
			if !ShownWellFraming(w).SameAs(saved) {
				t.Errorf("pane %vx%v: showing %+v and saving back what it shows stamped %+v",
					paneW, paneH, w, saved)
			}
		}
		// A root grid is entered by no doorway, so its unvisited view is the
		// origin at live zoom 1 rather than the preview calibration.
		overtake := Overtake(1, 1, paneW, paneH, cell)
		shown := ShownRootFraming(rpc.Framing{}, overtake)
		if !shown.SameAs(rpc.Framing{Zoom: IntrinsicFromLive(1, overtake)}) {
			t.Errorf("pane %vx%v: showing an unvisited root stamped %+v", paneW, paneH, shown)
		}
		// What the user does move is still a write.
		if shown.SameAs(rpc.Framing{Cx: 1, Zoom: shown.Zoom}) {
			t.Errorf("pane %vx%v: a pan of an unvisited root must still be written", paneW, paneH)
		}
		if shown.SameAs(rpc.Framing{Zoom: IntrinsicFromLive(2, overtake)}) {
			t.Errorf("pane %vx%v: a zoom of an unvisited root must still be written", paneW, paneH)
		}
	}
	if ShownWellFraming(Well{W: 1, H: 1}).
		SameAs(rpc.Framing{Cx: 0.5, Cy: 0.5, Zoom: 2 * DefaultWellViewZoom}) {
		t.Error("a reframe of an unvisited doorway must still be written")
	}
	// The stamp is what makes this matter: a root grid's would carry the
	// window it was looked at in into a window it was not.
	if ShownRootFraming(rpc.Framing{}, Overtake(1, 1, 1280, 800, cell)).
		SameAs(ShownRootFraming(rpc.Framing{}, Overtake(1, 1, 480, 900, cell))) {
		t.Fatal("the two panes above must disagree, or the case is not covered")
	}
}
