package anim

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLerpEndpoints(t *testing.T) {
	if !near(Lerp(2, 10, 0), 2) {
		t.Error("t=0 should give from")
	}
	if !near(Lerp(2, 10, 1), 10) {
		t.Error("t=1 should give to")
	}
	if !near(Lerp(2, 10, 0.5), 6) {
		t.Error("t=0.5 should give midpoint")
	}
}

func TestEaseOutCubicShape(t *testing.T) {
	// Endpoints.
	if EaseOutCubic(0) != 0 {
		t.Error("ease(0)")
	}
	if EaseOutCubic(1) != 1 {
		t.Error("ease(1)")
	}
	// Clamp outside [0, 1].
	if EaseOutCubic(-0.5) != 0 {
		t.Error("ease(-0.5) should clamp to 0")
	}
	if EaseOutCubic(1.5) != 1 {
		t.Error("ease(1.5) should clamp to 1")
	}
	// Monotonically increasing.
	prev := -1.0
	for i := 0; i <= 100; i++ {
		v := EaseOutCubic(float64(i) / 100)
		if v < prev {
			t.Fatalf("not monotonic at %d: %v < %v", i, v, prev)
		}
		prev = v
	}
	// Decelerating: derivative is non-increasing across the curve, so the
	// step from 0.0→0.1 should be larger than the step from 0.9→1.0.
	earlyStep := EaseOutCubic(0.1) - EaseOutCubic(0.0)
	lateStep := EaseOutCubic(1.0) - EaseOutCubic(0.9)
	if earlyStep <= lateStep {
		t.Errorf("expected ease-out: early=%v late=%v", earlyStep, lateStep)
	}
}

func TestProgressClamping(t *testing.T) {
	// Before start.
	if Progress(0, 100, 200) != 0 {
		t.Error("progress before start should be 0")
	}
	// Mid-animation.
	if !near(Progress(150, 100, 200), 0.25) {
		t.Errorf("progress mid: %v", Progress(150, 100, 200))
	}
	// After end.
	if Progress(500, 100, 200) != 1 {
		t.Error("progress after end should clamp to 1")
	}
	// Zero/negative duration: instant completion.
	if Progress(0, 0, 0) != 1 {
		t.Error("zero duration should be done")
	}
	if Progress(0, 0, -1) != 1 {
		t.Error("negative duration should be done")
	}
}

func TestAnimationAt(t *testing.T) {
	a := Animation{FromX: 0, FromY: 0, ToX: 100, ToY: 200, StartMs: 0, DurationMs: 1000}
	x, y, done := a.At(0)
	if !near(x, 0) || !near(y, 0) || done {
		t.Errorf("at start: x=%v y=%v done=%v", x, y, done)
	}
	x, y, done = a.At(1000)
	if !near(x, 100) || !near(y, 200) || !done {
		t.Errorf("at end: x=%v y=%v done=%v", x, y, done)
	}
	// Mid: with ease-out, value should be past the linear midpoint.
	x, _, _ = a.At(500)
	if x <= 50 {
		t.Errorf("at midpoint with ease-out, x=%v should be > 50 (linear midpoint)", x)
	}
}

func TestDistance(t *testing.T) {
	if !near(Distance(0, 0, 3, 4), 5) {
		t.Errorf("3-4-5 triangle")
	}
	if !near(Distance(0, 0, 0, 0), 0) {
		t.Errorf("zero distance")
	}
	if !near(Distance(-1, -1, 2, 3), 5) {
		t.Errorf("negative origin")
	}
}
