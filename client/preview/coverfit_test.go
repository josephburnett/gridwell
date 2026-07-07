package preview

import (
	"math"
	"testing"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestContainDstRectLetterboxesOneAxis(t *testing.T) {
	// Wide image (200x100, AR 2) into a square dest: full width, bars
	// above/below. dw=100, dh=50, centered vertically.
	dx, dy, dw, dh, ok := ContainDstRect(200, 100, 10, 20, 100, 100)
	if !ok || !almost(dx, 10) || !almost(dy, 45) || !almost(dw, 100) || !almost(dh, 50) {
		t.Errorf("wide image: (%v,%v,%v,%v) ok=%v", dx, dy, dw, dh, ok)
	}
	// Tall image (100x200) into a square dest: full height, bars left/right.
	dx, dy, dw, dh, _ = ContainDstRect(100, 200, 10, 20, 100, 100)
	if !almost(dx, 35) || !almost(dy, 20) || !almost(dw, 50) || !almost(dh, 100) {
		t.Errorf("tall image: (%v,%v,%v,%v)", dx, dy, dw, dh)
	}
	// Matching aspect: fills the dest exactly, no bars.
	dx, dy, dw, dh, _ = ContainDstRect(400, 300, 0, 0, 100, 75)
	if !almost(dx, 0) || !almost(dy, 0) || !almost(dw, 100) || !almost(dh, 75) {
		t.Errorf("matched aspect: (%v,%v,%v,%v)", dx, dy, dw, dh)
	}
}

// TestContainDstRectProperties: for arbitrary shapes, the image rect keeps
// the source aspect, stays inside the destination, is centered, and touches
// the destination on at least one full axis (bars never on both).
func TestContainDstRectProperties(t *testing.T) {
	shapes := []struct{ iw, ih, w, h float64 }{
		{1920, 1080, 64, 64}, {300, 900, 128, 64}, {50, 50, 640, 480},
		{7, 3, 3, 7}, {1024, 768, 1024, 768}, {2, 1000, 1000, 2},
	}
	for _, s := range shapes {
		dx, dy, dw, dh, ok := ContainDstRect(s.iw, s.ih, 5, 9, s.w, s.h)
		if !ok {
			t.Fatalf("%+v: not ok", s)
		}
		if !almost(dw/dh, s.iw/s.ih) {
			t.Errorf("%+v: aspect %v != source %v", s, dw/dh, s.iw/s.ih)
		}
		if dx < 5-1e-9 || dy < 9-1e-9 || dx+dw > 5+s.w+1e-9 || dy+dh > 9+s.h+1e-9 {
			t.Errorf("%+v: (%v,%v,%v,%v) escapes the destination", s, dx, dy, dw, dh)
		}
		if !almost(dx-5, 5+s.w-(dx+dw)) || !almost(dy-9, 9+s.h-(dy+dh)) {
			t.Errorf("%+v: not centered", s)
		}
		if !almost(dw, s.w) && !almost(dh, s.h) {
			t.Errorf("%+v: bars on both axes (dw=%v w=%v, dh=%v h=%v)", s, dw, s.w, dh, s.h)
		}
	}
}

func TestContainDstRectDegenerate(t *testing.T) {
	if _, _, _, _, ok := ContainDstRect(0, 100, 0, 0, 50, 50); ok {
		t.Error("zero-width image reported ok")
	}
	if _, _, _, _, ok := ContainDstRect(100, 100, 0, 0, 0, 50); ok {
		t.Error("zero-width dest reported ok")
	}
}
