package debounce

import "testing"

// queue is a Schedule that holds the pending runs instead of timing them, so a
// test decides when a window closes.
type queue struct {
	runs []func()
	ms   []int
}

func (q *queue) schedule(ms int, fire func()) {
	q.ms = append(q.ms, ms)
	q.runs = append(q.runs, fire)
}

// fireAll runs everything scheduled so far, in order. A body that arms again
// lands in the next round, not this one.
func (q *queue) fireAll() {
	runs := q.runs
	q.runs = nil
	for _, r := range runs {
		r()
	}
}

func TestBurstRunsOnce(t *testing.T) {
	q := &queue{}
	ran := 0
	d := New(q.schedule, func() { ran++ })
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
	d := New(q.schedule, func() { ran++ })
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
	d = New(q.schedule, func() {
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
	d := New(q.schedule, func() {})
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
