// Package debounce coalesces repeated requests into one deferred run. A
// Debounce holds its body from birth: only a run clears pending, so one armed
// bodiless would refuse every later arm.
package debounce

// Schedule defers fire by ms. The shim's is setTimeout; a test's is a queue.
type Schedule func(ms int, fire func())

// Clock reads the current time in milliseconds. A settle re-arms from its own
// run instead of cancelling, because the shim's Schedule is a bare setTimeout.
type Clock func() float64

// Mode is how a burst of arms lands on the window, with no default; see
// client/cadence.
type Mode int

const (
	// Throttle fixes the window at the first arm, so a burst runs once per
	// window while it lasts.
	Throttle Mode = iota
	// Settle restarts the window at every arm, so nothing runs until the arms
	// stop.
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
	// sig is what the last arm was keyed on; see ArmOnChange.
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

// ArmOnChange arms only when sig differs from the one the last arm carried,
// so a live tile repainting every frame does not hold a settle open forever.
func (d *Debounce) ArmOnChange(ms int, sig uint64) bool {
	if d.hasSig && d.sig == sig {
		return false
	}
	d.sig, d.hasSig = sig, true
	return d.Arm(ms)
}

// Pending reports whether a run is waiting.
func (d *Debounce) Pending() bool { return d.pending }

// fire clears pending before the body, so a body that arms from its own run
// opens a fresh window. A settle waits out a moved window unless under a
// millisecond remains, so a slipping clock cannot spin on zero-ms timers.
func (d *Debounce) fire() {
	if rest := d.due - d.now(); d.mode == Settle && rest >= 1 {
		d.schedule(int(rest), d.fire)
		return
	}
	d.pending = false
	d.body()
}
