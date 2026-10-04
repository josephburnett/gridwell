package rpc

import (
	"math"
	"testing"
)

// NewFraming is the one way to hold a Framing, so every value it refuses is
// one no writer can carry: a center that is not a point, a zoom that is not a
// size. "Never visited" is a View without a Framing, never a zero zoom.
func TestNewFramingRefusesWhatIsNotAView(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	for _, c := range [][3]float64{
		{nan, 0, 1}, {0, nan, 1}, {inf, 0, 1}, {0, -inf, 1},
		{0, 0, 0}, {0, 0, -1}, {0, 0, nan}, {0, 0, inf},
	} {
		if f, err := NewFraming(c[0], c[1], c[2]); err == nil {
			t.Errorf("NewFraming%v = %+v, want a refusal", c, f)
		}
	}
	f, err := NewFraming(-32.5, 3.7, 0.02)
	if err != nil || f.Cx() != -32.5 || f.Cy() != 3.7 || f.Zoom() != 0.02 {
		t.Errorf("NewFraming(-32.5, 3.7, 0.02) = %+v, %v", f, err)
	}
}

// A View reads absent wire fields, and anything NewFraming refuses, as never
// visited, and writes never visited back as the absent fields.
func TestViewOfReadsNoneAsAbsence(t *testing.T) {
	for _, c := range [][3]float64{{0, 0, 0}, {4.5, 3.5, 0}, {math.NaN(), 1, 1}} {
		v := ViewOf(c[0], c[1], c[2])
		if _, ok := v.Framing(); ok {
			t.Errorf("ViewOf%v has a framing", c)
		}
		if cx, cy, zoom := v.Wire(); cx != 0 || cy != 0 || zoom != 0 {
			t.Errorf("ViewOf%v.Wire() = %v %v %v, want the absent fields", c, cx, cy, zoom)
		}
		if !v.SameAs(View{}) {
			t.Errorf("ViewOf%v is not the same as none", c)
		}
	}
	v := ViewOf(1, 2, 0.5)
	if f, ok := v.Framing(); !ok || f.Zoom() != 0.5 || v.SameAs(View{}) {
		t.Errorf("ViewOf(1, 2, 0.5) = %+v", v)
	}
}
