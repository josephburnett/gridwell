// Package debounce coalesces repeated requests into one deferred run: the
// client's settle timers, which fire once after the last of a burst rather
// than once per frame.
//
// A Debounce holds what it runs from the moment it exists. Only a run clears
// the pending flag, so one armed before it had a body would refuse every arm
// after that, silently, for the life of the page.
package debounce

// Schedule defers fire by ms. The shim's is setTimeout; a test's is a queue.
type Schedule func(ms int, fire func())

// Debounce is not safe for concurrent use, the client being single-threaded.
type Debounce struct {
	schedule Schedule
	body     func()
	pending  bool
}

// New takes both halves at once; see the package comment for why.
func New(schedule Schedule, body func()) *Debounce {
	return &Debounce{schedule: schedule, body: body}
}

// Arm defers a run by ms, coalescing with one already waiting. It reports
// whether this call is the one that scheduled the run.
func (d *Debounce) Arm(ms int) bool {
	if d.pending {
		return false
	}
	d.pending = true
	d.schedule(ms, d.fire)
	return true
}

// Pending reports whether a run is waiting.
func (d *Debounce) Pending() bool { return d.pending }

// fire clears pending first, so a body that arms from inside its own run
// opens a fresh window instead of being swallowed.
func (d *Debounce) fire() {
	d.pending = false
	d.body()
}
