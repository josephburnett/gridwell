package debounce

import "testing"

// queue is a Schedule and a Clock together: it holds the pending runs and the
// virtual instant they are due, so a test decides when a window closes and how
// much of one an arm has left.
type queue struct {
	now  float64
	runs []func()
	dues []float64
}

func (q *queue) schedule(ms int, fire func()) {
	q.dues = append(q.dues, q.now+float64(ms))
	q.runs = append(q.runs, fire)
}

func (q *queue) clock() float64 { return q.now }

// fireAll runs everything scheduled so far, in order, whatever the clock says.
// A body that arms again lands in the next round, not this one.
func (q *queue) fireAll() {
	runs := q.runs
	q.runs, q.dues = nil, nil
	for _, r := range runs {
		r()
	}
}

// advance moves the clock and runs what came due, earliest first, including
// anything a run scheduled on its way past.
func (q *queue) advance(ms float64) {
	q.now += ms
	for {
		next := -1
		for i, due := range q.dues {
			if due <= q.now && (next < 0 || due < q.dues[next]) {
				next = i
			}
		}
		if next < 0 {
			return
		}
		fire := q.runs[next]
		q.dues = append(q.dues[:next], q.dues[next+1:]...)
		q.runs = append(q.runs[:next], q.runs[next+1:]...)
		fire()
	}
}

func newThrottle(q *queue, body func()) *Debounce { return New(q.schedule, q.clock, Throttle, body) }

func TestBurstRunsOnce(t *testing.T) {
	q := &queue{}
	ran := 0
	d := newThrottle(q, func() { ran++ })
	if !d.Arm(500) {
		t.Fatal("the first arm must schedule the run")
	}
	for i := 0; i < 5; i++ {
		if d.Arm(500) {
			t.Fatalf("arm %d scheduled a second run inside the window", i)
		}
	}
	if len(q.runs) != 1 {
		t.Fatalf("a burst scheduled %d runs, want 1", len(q.runs))
	}
	q.fireAll()
	if ran != 1 {
		t.Fatalf("body ran %d times, want 1", ran)
	}
}

func TestArmAgainAfterTheRun(t *testing.T) {
	q := &queue{}
	ran := 0
	d := newThrottle(q, func() { ran++ })
	d.Arm(10)
	q.fireAll()
	if d.Pending() {
		t.Fatal("a debounce that has run is not pending")
	}
	if !d.Arm(10) {
		t.Fatal("the arm after a run must schedule again")
	}
	q.fireAll()
	if ran != 2 {
		t.Fatalf("body ran %d times, want 2", ran)
	}
}

// The error-strip expiry arms itself from inside its own run. Clearing pending
// before the body is what lets that schedule a fresh window instead of being
// swallowed as a coalesce.
func TestBodyMayArmFromInsideItsOwnRun(t *testing.T) {
	q := &queue{}
	var d *Debounce
	ran := 0
	d = New(q.schedule, q.clock, Throttle, func() {
		ran++
		if ran < 3 {
			d.Arm(10)
		}
	})
	d.Arm(10)
	for i := 0; i < 5 && len(q.runs) > 0; i++ {
		q.fireAll()
	}
	if ran != 3 {
		t.Fatalf("body ran %d times, want 3", ran)
	}
}

// Pending and a scheduled run are one fact. An arm that marked a run pending
// without scheduling it would never clear the flag, and the debounce would
// refuse every later arm for the life of the page while the gesture-driven
// paths went on working — which is why a Debounce is given its body at
// construction and can never be in that state.
func TestPendingNeverOutrunsTheSchedule(t *testing.T) {
	q := &queue{}
	d := newThrottle(q, func() {})
	scheduled, fired := 0, 0
	for _, arm := range []bool{true, true, false, true, true, true, false} {
		if arm {
			if d.Arm(1) {
				scheduled++
			}
		} else {
			fired += len(q.runs)
			q.fireAll()
		}
		if d.Pending() != (scheduled > fired) {
			t.Fatalf("pending=%v with %d scheduled and %d fired", d.Pending(), scheduled, fired)
		}
	}
}

// A pan arms the framing persister on every frame it draws. Under a throttle
// the run came a fixed window after the first of those arms and the gesture
// was written four times; a settle has nothing to say until the arms stop,
// which is what "persists only its resting state" means.
func TestASettleRunsAfterTheLastArmOfABurst(t *testing.T) {
	q := &queue{}
	ran := 0
	d := New(q.schedule, q.clock, Settle, func() { ran++ })
	for i := 0; i < 20; i++ {
		d.Arm(600)
		q.advance(50)
		if ran != 0 {
			t.Fatalf("the run came %.0fms into a burst still arming every 50ms", q.now)
		}
	}
	// The window is measured from the last arm, at 950ms, and from no earlier
	// one: 549ms more leaves the clock a millisecond short of it.
	q.advance(549)
	if ran != 0 {
		t.Fatalf("the run came %.0fms after the last arm, want 600", q.now-950)
	}
	q.advance(2)
	if ran != 1 {
		t.Fatalf("after the burst the body ran %d times, want 1", ran)
	}
	if d.Pending() {
		t.Error("a settle that has run is still pending")
	}
}

// A throttle is the other half of the same table: the window is the first
// arm's, so a burst that never stops still runs once per window.
func TestAThrottleRunsWhileTheBurstLasts(t *testing.T) {
	q := &queue{}
	ran := 0
	d := newThrottle(q, func() { ran++ })
	for i := 0; i < 20; i++ {
		d.Arm(600)
		q.advance(50)
	}
	if ran != 1 {
		t.Fatalf("a 1000ms burst on a 600ms throttle ran %d times, want 1", ran)
	}
}

// One timer is alive at a time in either mode: the settle re-arms from inside
// its own run rather than scheduling a fresh timeout per arm, because every
// pending one holds a js.Func the shim cannot release until it fires.
func TestASettleHoldsOneTimerAtATime(t *testing.T) {
	q := &queue{}
	d := New(q.schedule, q.clock, Settle, func() {})
	for i := 0; i < 20; i++ {
		d.Arm(600)
		if len(q.runs) != 1 {
			t.Fatalf("arm %d left %d timers alive, want 1", i, len(q.runs))
		}
		q.advance(50)
	}
	q.advance(600)
	if len(q.runs) != 0 {
		t.Fatalf("%d timers outlived the run", len(q.runs))
	}
}

// The arm-on-change table. A live url or shell tile repaints on the mirror's
// own cadence for as long as it is open, and every one of those paints reaches
// the persisters; keyed on the fact instead of the frame, they arm nothing and
// the window the gesture opened closes on time. Without this, the pane-tile
// layout of a workspace holding a live shell never reached the server at all.
func TestArmOnChangeIgnoresARepaintThatPersistsNothingNew(t *testing.T) {
	q := &queue{}
	ran := 0
	d := New(q.schedule, q.clock, Settle, func() { ran++ })
	const window, mirror = 600, 250

	// The gesture: the fact moves on every frame, and each arm moves the
	// window with it.
	for i := 0; i < 5; i++ {
		d.ArmOnChange(window, uint64(i))
		q.advance(50)
	}
	if ran != 0 {
		t.Fatalf("the run came %.0fms into a gesture still changing the fact", q.now)
	}

	// The live tile: the same fact, once per mirror pass, for as long as it is
	// open. The window closes anyway, one window after the gesture's last arm.
	for i := 0; i < 40; i++ {
		d.ArmOnChange(window, 4)
		q.advance(mirror)
	}
	if ran != 1 {
		t.Fatalf("%.0fms of repaints on an unchanged fact ran the body %d times, want 1",
			q.now, ran)
	}

	// And the meter is live: the next real change arms again.
	d.ArmOnChange(window, 5)
	q.advance(window + 1)
	if ran != 2 {
		t.Fatalf("the change after the repaints ran the body %d times, want 2", ran)
	}
}

// The first arm has nothing to compare against, so a client that boots and
// never changes anything still writes what its first draw found.
func TestTheFirstArmOnChangeAlwaysArms(t *testing.T) {
	q := &queue{}
	ran := 0
	d := New(q.schedule, q.clock, Settle, func() { ran++ })
	if !d.ArmOnChange(600, 0) {
		t.Fatal("the first arm did not schedule a run")
	}
	q.advance(601)
	if ran != 1 {
		t.Fatalf("body ran %d times, want 1", ran)
	}
}
