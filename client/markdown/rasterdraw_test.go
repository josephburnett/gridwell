package markdown

import "testing"

// A rendered raster is drawn at the frame's scale whatever the box's width, so
// growing the box reveals more instead of stretching the letters; the box
// clips a wider raster and leaves a margin beside a narrower one.
func TestPreviewRasterDrawScaleIsTheFrames(t *testing.T) {
	const rasterW, rasterH = 192.0, 4000.0
	for _, c := range []struct {
		name      string
		w         float64
		wantSW    float64 // raster px shown: the raster clipped to the box
		wantSlack float64 // box px right of the raster: the margin
	}{
		{"narrower box clips", 150, 150, 0},
		{"exact box", 192, 192, 0},
		{"wider box leaves a margin", 250, 192, 58},
	} {
		f := PreviewWindowFrame(c.w, 1.0, 1.0, 0, 0)
		d, ok := PreviewRasterDraw(f, rasterW, rasterH, 10, 20, c.w, 100, 8)
		if !ok {
			t.Fatalf("%s: not drawn", c.name)
		}
		if !almost(d.DW/d.SW, 1.0) || !almost(d.DH/d.SH, 1.0) {
			t.Errorf("%s: drawn scale = %v x %v, want the frame's 1", c.name, d.DW/d.SW, d.DH/d.SH)
		}
		if !almost(d.SW, c.wantSW) || !almost(c.w-d.DW, c.wantSlack) {
			t.Errorf("%s: sw %v margin %v, want %v %v", c.name, d.SW, c.w-d.DW, c.wantSW, c.wantSlack)
		}
		// Below the banner, as tall as the box allows.
		if d.SX != 0 || d.SY != 0 || d.DX != 10 || d.DY != 28 || !almost(d.SH, 92) || !almost(d.DH, 92) {
			t.Errorf("%s: draw = %+v, want source (0,0), dest (10, 28), 92 tall", c.name, d)
		}
	}

	// content_zoom stays the owner of bigger letters: scale 2 shows half the
	// raster in the same box, twice the size.
	f := PreviewWindowFrame(150, 1.0, 2.0, 0, 0)
	d, _ := PreviewRasterDraw(f, rasterW, rasterH, 0, 0, 150, 100, 0)
	if !almost(d.DW/d.SW, 2) || !almost(d.SW, 75) || !almost(d.SH, 50) {
		t.Errorf("content zoom 2: %+v, want scale 2 over a 75x50 source", d)
	}
}

// The stored TextX/TextY place the window on the raster, and a window
// scrolled off it does not draw.
func TestPreviewRasterDrawHonoursTheStoredScroll(t *testing.T) {
	f := PreviewWindowFrame(100, 1.0, 1.0, 4, 30)
	d, ok := PreviewRasterDraw(f, 192, 120, 0, 0, 100, 200, 0)
	if !ok || d.SX != 4 || d.SY != 30 {
		t.Fatalf("scroll: %+v %v, want source from (4, 30)", d, ok)
	}
	if !almost(d.SH, 90) || !almost(d.DH, 90) {
		t.Errorf("a window past the raster's bottom draws only what is there: %+v", d)
	}
	for _, sy := range []int64{120, 500, -1} {
		if _, ok := PreviewRasterDraw(PreviewWindowFrame(100, 1, 1, 0, sy), 192, 120, 0, 0, 100, 200, 0); ok {
			t.Errorf("scrollY %d: drew a window off the raster", sy)
		}
	}
}
