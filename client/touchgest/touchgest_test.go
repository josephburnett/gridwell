package touchgest

import (
	"testing"
)

func pt(x, y float64) Point { return Point{X: x, Y: y} }

func kinds(as []Action) []Kind {
	out := make([]Kind, len(as))
	for i, a := range as {
		out[i] = a.Kind
	}
	return out
}

func wantActions(t *testing.T, got []Action, want ...Action) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("actions = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("action[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// ── single finger ────────────────────────────────────────────────────────────

func TestTapIsLeftClick(t *testing.T) {
	m := New()
	if as := m.Start([]Point{pt(100, 100)}, 0); len(as) != 0 {
		t.Fatalf("start emitted %+v; a press must stay silent until classified", as)
	}
	as := m.End(nil, 50)
	wantActions(t, as,
		Action{Kind: MouseDown, Pos: pt(100, 100), Button: 0},
		Action{Kind: MouseUp, Pos: pt(100, 100), Button: 0},
	)
}

func TestDragBeyondSlopIsLeftDrag(t *testing.T) {
	m := New()
	m.Start([]Point{pt(100, 100)}, 0)
	// Jitter within slop: still silent.
	if as := m.Move([]Point{pt(102, 101)}, 20); len(as) != 0 {
		t.Fatalf("within-slop move emitted %+v", as)
	}
	// Crossing slop: the press becomes a left drag anchored at the ORIGIN so
	// the downstream gesture engine sees the same press point a mouse would.
	as := m.Move([]Point{pt(100+SlopPx+2, 100)}, 40)
	wantActions(t, as,
		Action{Kind: MouseDown, Pos: pt(100, 100), Button: 0},
		Action{Kind: MouseMove, Pos: pt(100+SlopPx+2, 100), Button: 0},
	)
	as = m.Move([]Point{pt(140, 120)}, 60)
	wantActions(t, as, Action{Kind: MouseMove, Pos: pt(140, 120), Button: 0})
	as = m.End(nil, 80)
	wantActions(t, as, Action{Kind: MouseUp, Pos: pt(140, 120), Button: 0})
}

func TestLongPressIsRightButton(t *testing.T) {
	m := New()
	m.Start([]Point{pt(200, 200)}, 1000)
	// A stale timer from an earlier gesture (fired before HoldMs elapsed
	// for THIS press) must not classify.
	if as := m.Timer(1000 + HoldMs - 50); len(as) != 0 {
		t.Fatalf("early timer emitted %+v", as)
	}
	as := m.Timer(1000 + HoldMs)
	wantActions(t, as, Action{Kind: MouseDown, Pos: pt(200, 200), Button: 2})
	// Subsequent movement is a right drag (split/swap/clone/resize vocabulary).
	as = m.Move([]Point{pt(240, 200)}, 1000+HoldMs+50)
	wantActions(t, as, Action{Kind: MouseMove, Pos: pt(240, 200), Button: 2})
	as = m.End(nil, 1000+HoldMs+100)
	wantActions(t, as, Action{Kind: MouseUp, Pos: pt(240, 200), Button: 2})
}

func TestLongPressReleaseInPlaceIsRightClick(t *testing.T) {
	// Hold without moving, lift: right-click (e.g. on the corner circle =
	// ascend, matching the desktop right-click-corner gesture).
	m := New()
	m.Start([]Point{pt(10, 10)}, 0)
	m.Timer(HoldMs)
	as := m.End(nil, HoldMs+80)
	wantActions(t, as, Action{Kind: MouseUp, Pos: pt(10, 10), Button: 2})
}

func TestTimerAfterDragStartedDoesNotFireRight(t *testing.T) {
	m := New()
	m.Start([]Point{pt(0, 0)}, 0)
	m.Move([]Point{pt(50, 0)}, 30) // left drag begins
	if as := m.Timer(HoldMs + 1); len(as) != 0 {
		t.Fatalf("timer during left drag emitted %+v", as)
	}
}

// ── two fingers ──────────────────────────────────────────────────────────────

func TestTwoFingerTapIsMiddleClick(t *testing.T) {
	m := New()
	m.Start([]Point{pt(100, 100)}, 0)
	if as := m.Start([]Point{pt(100, 100), pt(140, 100)}, 30); len(as) != 0 {
		t.Fatalf("second finger emitted %+v", as)
	}
	// Both lift quickly with no movement → middle click at the midpoint (ascend).
	as := m.End(nil, 100)
	wantActions(t, as,
		Action{Kind: MouseDown, Pos: pt(120, 100), Button: 1},
		Action{Kind: MouseUp, Pos: pt(120, 100), Button: 1},
	)
}

func TestPinchEmitsWheelZoom(t *testing.T) {
	m := New()
	m.Start([]Point{pt(100, 100)}, 0)
	m.Start([]Point{pt(100, 100), pt(200, 100)}, 10) // dist 100, mid (150,100)
	// Spread apart past the classification lock: zoom in → negative deltaY
	// (wheel-up), anchored at the current midpoint.
	as := m.Move([]Point{pt(80, 100), pt(220, 100)}, 30) // dist 140
	if len(as) != 1 || as[0].Kind != Wheel {
		t.Fatalf("actions = %+v, want one Wheel", as)
	}
	if as[0].DeltaY >= 0 {
		t.Errorf("spread must zoom IN: deltaY = %v, want < 0", as[0].DeltaY)
	}
	if as[0].Pos != pt(150, 100) {
		t.Errorf("wheel anchored at %+v, want midpoint (150,100)", as[0].Pos)
	}
	// Closing the pinch zooms out → positive deltaY.
	as = m.Move([]Point{pt(90, 100), pt(210, 100)}, 50) // dist 120 < 140
	if len(as) != 1 || as[0].DeltaY <= 0 {
		t.Fatalf("closing pinch must zoom OUT: %+v", as)
	}
	// Lift: no synthetic clicks from a completed pinch.
	if as := m.End(nil, 80); len(as) != 0 {
		t.Fatalf("pinch end emitted %+v", as)
	}
}

func TestTwoFingerScrollEmitsWheel(t *testing.T) {
	m := New()
	m.Start([]Point{pt(100, 100)}, 0)
	m.Start([]Point{pt(100, 100), pt(140, 100)}, 10)
	// Both fingers travel up in parallel (distance constant): scroll. Fingers
	// moving up read content below → wheel-down (positive deltaY).
	as := m.Move([]Point{pt(100, 60), pt(140, 60)}, 30)
	if len(as) != 1 || as[0].Kind != Wheel {
		t.Fatalf("actions = %+v, want one Wheel", as)
	}
	if as[0].DeltaY <= 0 {
		t.Errorf("fingers up must scroll down: deltaY = %v, want > 0", as[0].DeltaY)
	}
}

func TestTwoFingerModeLocksOnce(t *testing.T) {
	// Once classified as a pinch, later parallel travel must keep zooming
	// (not flip to scroll mid-gesture).
	m := New()
	m.Start([]Point{pt(100, 100)}, 0)
	m.Start([]Point{pt(100, 100), pt(200, 100)}, 10)
	m.Move([]Point{pt(80, 100), pt(220, 100)}, 30) // locks pinch
	as := m.Move([]Point{pt(80, 60), pt(220, 60)}, 50)
	// Parallel move, distance unchanged → zero pinch delta → no action (not a scroll).
	if len(as) != 0 {
		t.Fatalf("locked pinch emitted %+v for a parallel move", as)
	}
}

func TestLongPressTimerIgnoredInTwoFingerMode(t *testing.T) {
	m := New()
	m.Start([]Point{pt(0, 0)}, 0)
	m.Start([]Point{pt(0, 0), pt(40, 0)}, 10)
	if as := m.Timer(HoldMs); len(as) != 0 {
		t.Fatalf("timer during two-finger gesture emitted %+v", as)
	}
}

// ── ending / stray fingers ───────────────────────────────────────────────────

func TestOneFingerRemainingAfterTwoIsDeadUntilAllLift(t *testing.T) {
	m := New()
	m.Start([]Point{pt(100, 100)}, 0)
	m.Start([]Point{pt(100, 100), pt(200, 100)}, 10)
	m.Move([]Point{pt(80, 100), pt(220, 100)}, 30) // pinch
	// One finger lifts; the survivor must NOT start panning.
	if as := m.End([]Point{pt(80, 100)}, 50); len(as) != 0 {
		t.Fatalf("first lift emitted %+v", as)
	}
	if as := m.Move([]Point{pt(300, 300)}, 70); len(as) != 0 {
		t.Fatalf("survivor movement emitted %+v", as)
	}
	if as := m.End(nil, 90); len(as) != 0 {
		t.Fatalf("final lift emitted %+v", as)
	}
	// The machine is idle again: a fresh tap works.
	m.Start([]Point{pt(5, 5)}, 100)
	as := m.End(nil, 120)
	wantActions(t, as,
		Action{Kind: MouseDown, Pos: pt(5, 5), Button: 0},
		Action{Kind: MouseUp, Pos: pt(5, 5), Button: 0},
	)
}

func TestExtraFingerDuringDragEndsTheDrag(t *testing.T) {
	m := New()
	m.Start([]Point{pt(0, 0)}, 0)
	m.Move([]Point{pt(50, 0)}, 20) // left drag at (50,0)
	as := m.Start([]Point{pt(50, 0), pt(90, 0)}, 40)
	wantActions(t, as, Action{Kind: MouseUp, Pos: pt(50, 0), Button: 0})
	// And the machine waits for all fingers to lift.
	if as := m.Move([]Point{pt(60, 0), pt(100, 0)}, 60); len(as) != 0 {
		t.Fatalf("dead-state move emitted %+v", as)
	}
}

func TestThreeFingersGoDead(t *testing.T) {
	m := New()
	m.Start([]Point{pt(0, 0)}, 0)
	m.Start([]Point{pt(0, 0), pt(40, 0)}, 10)
	if as := m.Start([]Point{pt(0, 0), pt(40, 0), pt(80, 0)}, 20); len(as) != 0 {
		t.Fatalf("third finger emitted %+v", as)
	}
	if as := m.Move([]Point{pt(10, 0), pt(50, 0), pt(90, 0)}, 40); len(as) != 0 {
		t.Fatalf("three-finger move emitted %+v", as)
	}
}
