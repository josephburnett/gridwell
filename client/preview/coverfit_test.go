package preview

import (
	"math"
	"testing"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCoverSrcRectNoPan(t *testing.T) {
	// Image 200x100 (AR 2) into dest 100x100 (AR 1): image is wider than
	// dest, so crop width. sh = 100 (full height), sw = 100*1 = 100,
	// centered: sx = (200-100)/2 = 50, sy = 0.
	sx, sy, sw, sh, ok := CoverSrcRect(200, 100, 100, 100, 0, 0)
	if !ok || !almost(sx, 50) || !almost(sy, 0) || !almost(sw, 100) || !almost(sh, 100) {
		t.Errorf("wide image: (%v,%v,%v,%v) ok=%v", sx, sy, sw, sh, ok)
	}
	// Image 100x200 (AR .5) into dest 100x100: taller than dest, crop height.
	// sw = 100, sh = 100, centered: sx=0, sy=(200-100)/2=50.
	sx, sy, sw, sh, _ = CoverSrcRect(100, 200, 100, 100, 0, 0)
	if !almost(sx, 0) || !almost(sy, 50) || !almost(sw, 100) || !almost(sh, 100) {
		t.Errorf("tall image: (%v,%v,%v,%v)", sx, sy, sw, sh)
	}
}

func TestCoverSrcRectPan(t *testing.T) {
	// Wide 200x100 into 100x100: scale = sw/w = 100/100 = 1 src-px/dest-px.
	// panX 10 dest-px -> +10 src-px from the centered sx=50 -> 60.
	sx, _, _, _, _ := CoverSrcRect(200, 100, 100, 100, 10, 0)
	if !almost(sx, 60) {
		t.Errorf("panned sx = %v, want 60", sx)
	}
}

func TestCoverSrcRectDegenerate(t *testing.T) {
	sx, sy, sw, sh, ok := CoverSrcRect(0, 100, 100, 100, 0, 0)
	if ok || sw != 0 || sh != 100 || sx != 0 || sy != 0 {
		t.Errorf("degenerate image should report ok=false, got (%v,%v,%v,%v) ok=%v", sx, sy, sw, sh, ok)
	}
	if _, _, _, _, ok := CoverSrcRect(200, 100, 0, 100, 0, 0); ok {
		t.Error("degenerate dest width should report ok=false")
	}
}

func TestClampPan(t *testing.T) {
	// Wide 200x100 into 100x100: scale=1, slack = (200-100)/2 = 50 src-px,
	// over scale 1 => maxPanX 50; vertically sh=100=ih so maxPanY 0.
	px, py := ClampPan(1000, 1000, 200, 100, 100, 100)
	if !almost(px, 50) || !almost(py, 0) {
		t.Errorf("clamp hi = (%v,%v), want (50,0)", px, py)
	}
	px, py = ClampPan(-1000, -1000, 200, 100, 100, 100)
	if !almost(px, -50) || !almost(py, 0) {
		t.Errorf("clamp lo = (%v,%v), want (-50,0)", px, py)
	}
	// In-range pan passes through.
	px, _ = ClampPan(20, 0, 200, 100, 100, 100)
	if !almost(px, 20) {
		t.Errorf("in-range pan = %v, want 20", px)
	}
	// Degenerate -> no pan.
	if px, py := ClampPan(5, 5, 0, 0, 100, 100); px != 0 || py != 0 {
		t.Errorf("degenerate clamp = (%v,%v), want (0,0)", px, py)
	}
}

// CoverSrcRect and ClampPan must agree: panning to the clamp limit lands the
// source rect exactly flush with the image edge (sx == 0 on the left edge).
func TestCoverFitConsistency(t *testing.T) {
	const iw, ih, w, h = 200.0, 100.0, 100.0, 100.0
	px, _ := ClampPan(-1000, 0, iw, ih, w, h) // pan fully left
	sx, _, _, _, _ := CoverSrcRect(iw, ih, w, h, px, 0)
	if !almost(sx, 0) {
		t.Errorf("max-left pan should put sx at 0, got %v", sx)
	}
	px, _ = ClampPan(1000, 0, iw, ih, w, h) // pan fully right
	sx, _, sw, _, _ := CoverSrcRect(iw, ih, w, h, px, 0)
	if !almost(sx+sw, iw) {
		t.Errorf("max-right pan should put sx+sw at iw=%v, got %v", iw, sx+sw)
	}
}
