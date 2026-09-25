// Package debounce coalesces repeated requests into one deferred run: the
// client's settle timers, which fire once after a burst of arms rather than
// once per frame.
//
// A Debounce holds what it runs from the moment it exists. Only a run clears
// the pending flag, so one armed before it had a body would refuse every arm
// after that, silently, for the life of the page.
package debounce

// Schedule defers fire by ms. The shim's is setTimeout; a test's is a queue.
type Schedule func(ms int, fire func())

// Clock reads the current time in milliseconds. A settle needs it because it
// re-arms from inside its own run instead of cancelling: the shim's Schedule
// is a bare setTimeout, and a cancel would have to pair a clearTimeout with
// the js.Func release that only a call performs.
type Clock func() float64

// Mode is how a burst of arms lands on the window. It is the caller's
// declaration and this package has no default: client/cadence writes each
// wait's mode beside the sentence that asks for it.
type Mode int

const (
	// Throttle fixes the window at the first arm, so a burst runs once per
	// window while it lasts. A writer with something to say at every moment
	// of a gesture is a throttle.
	Throttle Mode = iota
	// Settle restarts the window at every arm, so nothing runs until the arms
	// stop. A persister of a resting state is a settle: mid-gesture it has
	// nothing to say, and saying it costs a write, an event back, and a
	// repaint.
	Settle
)

// Debounce is not safe for concurrent use, the client being single-threaded.
type Debounce struct {
	schedule Schedule
	now      Clock
	mode     Mode
	body     func()
	pending  bool
	// due is when the latest arm wants the run; only a settle waits for it.
	due float64
	// sig is what the last arm was keyed on, for the callers that arm on a
	// change rather than on an event; see ArmOnChange.
	sig    uint64
	hasSig bool
}

// New takes every half at once; see the package comment for why.
func New(schedule Schedule, now Clock, mode Mode, body func()) *Debounce {
	return &Debounce{schedule: schedule, now: now, mode: mode, body: body}
}

// Arm defers a run by ms, coalescing with one already waiting. It reports
// whether this call is the one that scheduled the run.
func (d *Debounce) Arm(ms int) bool {
	d.due = d.now() + float64(ms)
	if d.pending {
		return false
	}
	d.pending = true
	d.schedule(ms, d.fire)
	return true
}

// ArmOnChange arms only when sig differs from the one the last arm carried.
// A caller that arms once per frame is keyed on frames stopping, which a live
// url or shell tile repainting on the mirror's cadence never lets happen;
// keyed on what the run would write, a repaint that changes nothing arms
// nothing and the window a gesture opened closes on time. The signature is the
// caller's fact: client/pane.PersistedFingerprint is the persisters'.
func (d *Debounce) ArmOnChange(ms int, sig uint64) bool {
	if d.hasSig && d.sig == sig {
		return false
	}
	d.sig, d.hasSig = sig, true
	return d.Arm(ms)
}

// Pending reports whether a run is waiting.
func (d *Debounce) Pending() bool { return d.pending }

// fire clears pending before the body, so a body that arms from inside its own
// run opens a fresh window instead of being swallowed. A settle whose window
// moved under it waits out the remainder; under a millisecond it just runs,
// which is also what keeps a slipping clock from spinning on zero-ms timers.
func (d *Debounce) fire() {
	if rest := d.due - d.now(); d.mode == Settle && rest >= 1 {
		d.schedule(int(rest), d.fire)
		return
	}
	d.pending = false
	d.body()
}
