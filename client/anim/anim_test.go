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
	if EaseOutCubic(0) != 0 {
		t.Error("ease(0)")
	}
	if EaseOutCubic(1) != 1 {
		t.Error("ease(1)")
	}
	if EaseOutCubic(-0.5) != 0 {
		t.Error("ease(-0.5) should clamp to 0")
	}
	if EaseOutCubic(1.5) != 1 {
		t.Error("ease(1.5) should clamp to 1")
	}
	prev := -1.0
	for i := 0; i <= 100; i++ {
		v := EaseOutCubic(float64(i) / 100)
		if v < prev {
			t.Fatalf("not monotonic at %d: %v < %v", i, v, prev)
		}
		prev = v
	}
	// Ease-out means the derivative never rises.
	earlyStep := EaseOutCubic(0.1) - EaseOutCubic(0.0)
	lateStep := EaseOutCubic(1.0) - EaseOutCubic(0.9)
	if earlyStep <= lateStep {
		t.Errorf("expected ease-out: early=%v late=%v", earlyStep, lateStep)
	}
}

func TestProgressClamping(t *testing.T) {
	if Progress(0, 100, 200) != 0 {
		t.Error("progress before start should be 0")
	}
	if !near(Progress(150, 100, 200), 0.25) {
		t.Errorf("progress mid: %v", Progress(150, 100, 200))
	}
	if Progress(500, 100, 200) != 1 {
		t.Error("progress after end should clamp to 1")
	}
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
	x, _, _ = a.At(500)
	if x <= 50 {
		t.Errorf("at midpoint with ease-out, x=%v should be > 50 (linear midpoint)", x)
	}
}

func TestSplitN(t *testing.T) {
	got := SplitN([]float64{1, 1, 1}, 300)
	if !near(got[0], 100) || !near(got[1], 100) || !near(got[2], 100) {
		t.Errorf("equal: %v", got)
	}
	got = SplitN([]float64{2, 3, 5}, 100)
	sum := 0.0
	for _, v := range got {
		sum += v
	}
	if !near(sum, 100) {
		t.Errorf("sum drift: %v", sum)
	}
	got = SplitN([]float64{0, 1, 0}, 200)
	if got[0] != 0 || got[2] != 0 {
		t.Errorf("zero phase got time: %v", got)
	}
	if !near(got[1], 200) {
		t.Errorf("middle phase: %v", got[1])
	}
	// A trailing zero-distance phase, the instant install segment of every
	// descent, must be exactly zero, so the rounding remainder goes to the last
	// phase with distance. Non-round inputs surface the floating-point error
	// the round cases hide.
	for _, d := range []float64{37.3, 99.9, 0.123} {
		z := SplitN([]float64{d, 0}, 250)
		if z[1] != 0 {
			t.Errorf("SplitN([%v,0],250) trailing zero phase = %v, want exactly 0", d, z[1])
		}
		if sum := z[0] + z[1]; !near(sum, 250) {
			t.Errorf("SplitN([%v,0],250) sum = %v, want 250", d, sum)
		}
	}
	got = SplitN([]float64{0, 0, 0}, 90)
	for _, v := range got {
		if !near(v, 30) {
			t.Errorf("equal fallback: %v", got)
			break
		}
	}
	if got := SplitN(nil, 100); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}

func TestLerpExpEndpointsAndShape(t *testing.T) {
	if !near(LerpExp(1, 8, 0), 1) {
		t.Error("LerpExp at t=0")
	}
	if !near(LerpExp(1, 8, 1), 8) {
		t.Error("LerpExp at t=1")
	}
	// The midpoint is geometric, not arithmetic.
	if !near(LerpExp(1, 4, 0.5), 2) {
		t.Errorf("LerpExp(1,4,0.5)=%v, want 2", LerpExp(1, 4, 0.5))
	}
	if !near(LerpExp(0.25, 4, 0.5), 1) {
		t.Errorf("LerpExp(0.25,4,0.5)=%v, want 1", LerpExp(0.25, 4, 0.5))
	}
	if !near(LerpExp(0, 10, 0.5), 5) {
		t.Errorf("fallback linear")
	}
}

func TestFadeAlpha(t *testing.T) {
	if got := FadeAlpha(1000, 1000, 2000); got != 1 {
		t.Errorf("at start: alpha = %v, want 1", got)
	}
	if got := FadeAlpha(3000, 1000, 2000); got != 0 {
		t.Errorf("at end: alpha = %v, want 0", got)
	}
	if got := FadeAlpha(9000, 1000, 2000); got != 0 {
		t.Errorf("past end: alpha = %v, want 0", got)
	}
	prev := 1.1
	for ms := 0.0; ms <= 2000; ms += 100 {
		a := FadeAlpha(1000+ms, 1000, 2000)
		if a < 0 || a > 1 {
			t.Fatalf("alpha out of range at +%vms: %v", ms, a)
		}
		if a >= prev {
			t.Fatalf("alpha not strictly decreasing at +%vms: %v >= %v", ms, a, prev)
		}
		prev = a
	}
}

func TestFlightLandsAndEnds(t *testing.T) {
	var f Flight
	f.Land(10, 20, 100, 50)
	if landing, done := f.Step(125); !landing || done {
		t.Fatalf("mid-landing: landing=%v done=%v", landing, done)
	}
	if landing, done := f.Step(150); !landing || !done || !near(f.X, 10) || !near(f.Y, 20) {
		t.Fatalf("at the end: landing=%v done=%v at (%v,%v)", landing, done, f.X, f.Y)
	}
}

// A press during an older drop's landing makes a new flight; the older
// landing, stepped to its end, neither moves nor ends the new one.
func TestALandingEndsOnlyItsOwnFlight(t *testing.T) {
	var old Flight
	old.Land(10, 20, 100, 50)
	held := Flight{X: 300, Y: 400}
	for _, now := range []float64{110, 150, 1000} {
		old.Step(now)
		if landing, done := held.Step(now); landing || done || held.X != 300 || held.Y != 400 {
			t.Fatalf("at %v the held flight moved or ended: landing=%v done=%v at (%v,%v)",
				now, landing, done, held.X, held.Y)
		}
	}
}

// A carried member sent back flies home on its own, from where it is drawn,
// growing to its home cell size, while the rest land with the lead; the
// formation ends only when the lead and every member sent back have.
func TestFormationReturnsOneMemberAlone(t *testing.T) {
	f := Formation{Flight: Flight{X: 0, Y: 0}}
	f.Land(100, 0, 0, 100)
	if _, ok := f.Returning(1); ok {
		t.Fatal("a member returning before it was sent")
	}
	f.Step(50)
	f.Return(1, f.X+10, f.Y, 10, 0, 0, 20, 50, 200)
	if _, ok := f.Returning(0); ok {
		t.Fatal("the lead returned with its member")
	}
	if landing, done := f.Step(100); !landing || done {
		t.Fatalf("the lead landed but a member is still flying: landing=%v done=%v", landing, done)
	}
	if !near(f.X, 100) {
		t.Errorf("the lead is at %v, want 100", f.X)
	}
	r, _ := f.Returning(1)
	if r.X <= 0 || r.X >= f.X+10 || r.Cell <= 10 || r.Cell >= 20 {
		t.Errorf("mid-return the member is at %v, cell %v", r.X, r.Cell)
	}
	if landing, done := f.Step(250); !landing || !done || !near(r.X, 0) || !near(r.Cell, 20) {
		t.Fatalf("at the end: landing=%v done=%v at %v cell %v", landing, done, r.X, r.Cell)
	}
}

// Sending a member back twice starts the second from where the first left it.
func TestFormationReturnRestartsWhereItIs(t *testing.T) {
	var f Formation
	f.Land(0, 0, 0, 10)
	f.Return(0, 100, 0, 10, 0, 0, 10, 0, 100)
	f.Step(50)
	r, _ := f.Returning(0)
	mid := r.X
	f.Return(0, 999, 999, 10, 0, 0, 10, 50, 100)
	if r, _ = f.Returning(0); r.X != mid {
		t.Errorf("the second return jumped to %v from %v", r.X, mid)
	}
}
