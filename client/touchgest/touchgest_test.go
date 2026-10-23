package touchgest

import (
	"testing"
)

func pt(x, y float64) Point { return Point{X: x, Y: y} }

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
	// Crossing slop makes the press a left drag anchored at the origin, the
	// same press point a mouse would give.
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
	// A timer that fires before HoldMs elapsed for this press must not classify.
	if as := m.Timer(1000 + HoldMs - 50); len(as) != 0 {
		t.Fatalf("early timer emitted %+v", as)
	}
	as := m.Timer(1000 + HoldMs)
	wantActions(t, as, Action{Kind: MouseDown, Pos: pt(200, 200), Button: 2})
	// Subsequent movement is a right drag.
	as = m.Move([]Point{pt(240, 200)}, 1000+HoldMs+50)
	wantActions(t, as, Action{Kind: MouseMove, Pos: pt(240, 200), Button: 2})
	as = m.End(nil, 1000+HoldMs+100)
	wantActions(t, as, Action{Kind: MouseUp, Pos: pt(240, 200), Button: 2})
}

func TestLongPressReleaseInPlaceSelectsThenReleasesRight(t *testing.T) {
	// Hold without moving and lift selects what the right press holds, then
	// ends the right click, matching the desktop right-click-corner gesture.
	// No left press follows: the right click is all a mouse would have sent.
	m := New()
	m.Start([]Point{pt(10, 10)}, 0)
	m.Timer(HoldMs)
	m.Move([]Point{pt(10+SlopPx-1, 10)}, HoldMs+20) // jitter within slop
	as := m.End(nil, HoldMs+80)
	wantActions(t, as,
		Action{Kind: Select, Pos: pt(10, 10)},
		Action{Kind: MouseUp, Pos: pt(10+SlopPx-1, 10), Button: 2},
	)
}

// A long-press that strayed and came back was a right drag, never a select.
func TestLongPressThatStrayedIsNoSelect(t *testing.T) {
	m := New()
	m.Start([]Point{pt(10, 10)}, 0)
	m.Timer(HoldMs)
	m.Move([]Point{pt(60, 10)}, HoldMs+20)
	m.Move([]Point{pt(10, 10)}, HoldMs+40)
	as := m.End(nil, HoldMs+80)
	wantActions(t, as, Action{Kind: MouseUp, Pos: pt(10, 10), Button: 2})

	// The next hold starts clean.
	m.Start([]Point{pt(10, 10)}, 1000)
	m.Timer(1000 + HoldMs)
	if as := m.End(nil, 1000+HoldMs+80); len(as) != 2 || as[0].Kind != Select {
		t.Errorf("the next in-place hold = %+v, want the select", as)
	}
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
	// Both lift quickly with no movement: a middle click at the midpoint.
	as := m.End(nil, 100)
	wantActions(t, as,
		Action{Kind: MouseDown, Pos: pt(120, 100), Button: 1},
		Action{Kind: MouseUp, Pos: pt(120, 100), Button: 1},
	)
}

func TestTwoFingerTapWithStaggeredLifts(t *testing.T) {
	// Hardware and CDP injection report one touchend per finger, so the machine
	// sees End([p1]) then End([]). The tap must still classify.
	m := New()
	m.Start([]Point{pt(100, 100)}, 0)
	m.Start([]Point{pt(100, 100), pt(140, 100)}, 30)
	if as := m.End([]Point{pt(100, 100)}, 80); len(as) != 0 {
		t.Fatalf("first staggered lift emitted %+v", as)
	}
	as := m.End(nil, 100)
	wantActions(t, as,
		Action{Kind: MouseDown, Pos: pt(120, 100), Button: 1},
		Action{Kind: MouseUp, Pos: pt(120, 100), Button: 1},
	)
}

func TestSlowStaggeredLiftIsNotATap(t *testing.T) {
	m := New()
	m.Start([]Point{pt(100, 100)}, 0)
	m.Start([]Point{pt(100, 100), pt(140, 100)}, 30)
	m.End([]Point{pt(100, 100)}, 80)
	// The last finger lifts past the tap window, so there is no click.
	if as := m.End(nil, 30+TwoTapMs+50); len(as) != 0 {
		t.Fatalf("slow lift emitted %+v", as)
	}
}

func TestMovedTwoFingerStaggeredLiftIsNotATap(t *testing.T) {
	// A pinch or scroll that lifts staggered must not fire a phantom middle
	// click on the final lift.
	m := New()
	m.Start([]Point{pt(100, 100)}, 0)
	m.Start([]Point{pt(100, 100), pt(200, 100)}, 10)
	m.Move([]Point{pt(80, 100), pt(220, 100)}, 30) // locked pinch
	m.End([]Point{pt(80, 100)}, 50)
	if as := m.End(nil, 70); len(as) != 0 {
		t.Fatalf("pinch staggered lift emitted %+v", as)
	}
}

func TestTwoFingerStartFromIdle(t *testing.T) {
	// Both fingers can arrive in one event, or the first landed on a DOM overlay
	// (the file textarea) that forwards only multi-finger touches, so the
	// machine never saw a single-finger start. A two-finger tap must still
	// classify.
	m := New()
	if as := m.Start([]Point{pt(100, 100), pt(140, 100)}, 0); len(as) != 0 {
		t.Fatalf("two-finger start emitted %+v", as)
	}
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
	// Spreading past the classification lock zooms in, so deltaY is negative,
	// anchored at the current midpoint.
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
	// Closing the pinch zooms out, so deltaY is positive.
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
	// Both fingers travel up in parallel with the distance constant, which is a
	// scroll. Fingers moving up read content below, so deltaY is positive.
	as := m.Move([]Point{pt(100, 60), pt(140, 60)}, 30)
	if len(as) != 1 || as[0].Kind != Wheel {
		t.Fatalf("actions = %+v, want one Wheel", as)
	}
	if as[0].DeltaY <= 0 {
		t.Errorf("fingers up must scroll down: deltaY = %v, want > 0", as[0].DeltaY)
	}
}

func TestTwoFingerModeLocksOnce(t *testing.T) {
	// Once classified as a pinch, later parallel travel keeps zooming.
	m := New()
	m.Start([]Point{pt(100, 100)}, 0)
	m.Start([]Point{pt(100, 100), pt(200, 100)}, 10)
	m.Move([]Point{pt(80, 100), pt(220, 100)}, 30) // locks pinch
	as := m.Move([]Point{pt(80, 60), pt(220, 60)}, 50)
	// A parallel move leaves the distance unchanged, so there is no action.
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
	// One finger lifts; the survivor must not start panning.
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
