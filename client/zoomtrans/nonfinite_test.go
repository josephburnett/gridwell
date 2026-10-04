package zoomtrans

import (
	"math"
	"math/rand"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// A pane left at live zoom 0 turned its center NaN on the first wheel notch:
// the cursor's cell is an infinite distance out and the ratio is 0. That NaN
// is what the 2026-10-04 trace persisted onto the home root. A wheel on a
// view that is not one refuses rather than computes.
func TestWheelZoomFromZeroStaysFinite(t *testing.T) {
	cx, cy := -32.5, -122.6
	// The cursor's cell as dragdrop.Pane.ScreenToCell computed it at zoom 0.
	zoom := 0.0
	cellX, cellY := cx+120/(64*zoom), cy+40/(64*zoom)
	z, ncx, ncy, ok := WheelZoom(-100, zoom, cx, cy, cellX, cellY, 1.1, 0.03125, 8)
	if ok {
		t.Errorf("WheelZoom from zoom 0 = (%v, %v, %v), want a refusal", z, ncx, ncy)
	}
	if _, _, _, ok := WheelZoom(-100, 1, cx, cy, math.Inf(1), cellY, 1.1, 0.03125, 8); ok {
		t.Error("WheelZoom about a cursor at infinity did not refuse")
	}
}

// A pane with no rect has no Size, so it has no framing to write: the empty
// rect's overtake of 0 made an intrinsic zoom of 0, which erased the
// doorway's framing and, never SameAs what the row then showed, went out
// again on every settle (the trace's repeated identical "zoom 0" persists).
// A writeback that does land is a no-op the second time.
func TestDoorwayWritebackInAnEmptyRectSettles(t *testing.T) {
	if s, ok := SizeOf(0, 0); ok {
		t.Fatalf("SizeOf(0, 0) = %+v, want none", s)
	}
	for _, c := range [][2]float64{{0, 600}, {800, 0}, {-1, 600}, {math.NaN(), 600}, {800, math.Inf(1)}} {
		if s, ok := SizeOf(c[0], c[1]); ok {
			t.Errorf("SizeOf%v = %+v, want none", c, s)
		}
	}
	door := Well{ID: "w8", W: 9, H: 7, View: rpc.ViewOf(0.43, 3.7, 0.6575)}
	pane := sz(800, 600)
	sv, _ := StoredView(door, pane, 64)
	cx, cy, live := sv.Cx(), sv.Cy(), sv.Zoom()
	if f, ok := Writeback(ShownWellFraming(door), door, cx, cy, live, pane, 64); ok {
		t.Errorf("an untouched doorway wrote %+v", f)
	}
	if f, ok := Writeback(ShownWellFraming(door), door, cx, cy, 0, pane, 64); ok {
		t.Errorf("a live zoom of 0 wrote %+v", f)
	}
	next, ok := Writeback(ShownWellFraming(door), door, cx+1, cy, live*2, pane, 64)
	if !ok {
		t.Fatal("a pan and zoom wrote nothing")
	}
	door.View = rpc.Saved(next)
	if f, ok := Writeback(ShownWellFraming(door), door, cx+1, cy, live*2, pane, 64); ok {
		t.Errorf("the same view wrote again: %+v", f)
	}
}

// Any finite input measured against a non-empty Size yields a framing that is
// one, or none at all: never a NaN, an infinity, or a zoom at or below zero.
func TestFramingMathIsFiniteOrRefused(t *testing.T) {
	rng := rand.New(rand.NewSource(20261004))
	pick := func() float64 {
		switch rng.Intn(8) {
		case 0:
			return 0
		case 1:
			return -rng.Float64() * 1e6
		case 2:
			return rng.Float64() * 1e-9
		case 3:
			return rng.Float64() * 1e9
		case 4:
			return math.MaxFloat64 / 2
		default:
			return (rng.Float64() - 0.5) * 200
		}
	}
	finite := func(name string, vs ...float64) {
		t.Helper()
		for _, v := range vs {
			if !rpc.Finite(v) {
				t.Fatalf("%s: %v", name, vs)
			}
		}
	}
	positive := func(name string, zoom float64) {
		t.Helper()
		if !(zoom > 0) || !rpc.Finite(zoom) {
			t.Fatalf("%s: zoom %v", name, zoom)
		}
	}
	for i := 0; i < 20000; i++ {
		s, ok := SizeOf(math.Abs(pick()), math.Abs(pick()))
		if !ok {
			continue
		}
		w := Well{X: int64(pick()), Y: int64(pick()), W: 1 + int64(rng.Intn(20)), H: 1 + int64(rng.Intn(20)),
			View: rpc.ViewOf(pick(), pick(), pick())}
		from := Endpoints{Cx: pick(), Cy: pick(), Zoom: math.Abs(pick())}
		if sv, ok := StoredView(w, s, 64); ok {
			finite("StoredView", sv.Cx(), sv.Cy())
			positive("StoredView", sv.Zoom())
			from = Endpoints{Cx: sv.Cx(), Cy: sv.Cy(), Zoom: sv.Zoom()}
		}
		if !from.view() {
			continue
		}
		if mid, swap, final, ok := Descent(from, w, s, 64); ok {
			finite("Descent", mid.Cx, mid.Cy, swap.Cx, swap.Cy, final.Cx, final.Cy)
			positive("Descent mid", mid.Zoom)
			positive("Descent swap", swap.Zoom)
			positive("Descent final", final.Zoom)
		}
		if amid, to, ok := Ascent(from, w, nil, s, 64); ok {
			finite("Ascent", amid.Cx, amid.Cy, to.Cx, to.Cy)
			positive("Ascent mid", amid.Zoom)
			positive("Ascent to", to.Zoom)
		}
		if f, ok := Writeback(ShownWellFraming(w), w, pick(), pick(), pick(), s, 64); ok {
			finite("Writeback", f.Cx(), f.Cy())
			positive("Writeback", f.Zoom())
		}
		if z, ncx, ncy, ok := WheelZoom(pick(), pick(), pick(), pick(), pick(), pick(), 1.1, 1.0/64, 64); ok {
			finite("WheelZoom", ncx, ncy)
			positive("WheelZoom", z)
		}
	}
}
