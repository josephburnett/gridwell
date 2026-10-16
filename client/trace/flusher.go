package trace

import (
	"time"

	"github.com/josephburnett/gridwell/client/cadence"
	"github.com/josephburnett/gridwell/client/debounce"
)

// Flusher is the flush loop: the ring, the pump, and its one timer. Every
// pass that leaves a record owed and no post out arms the timer for the
// moment the pump says the next post is due, so a record never waits on an
// emit that may not come.
type Flusher struct {
	c     *Client
	pump  *Pump
	timer *debounce.Debounce
	now   func() time.Time
	post  func(batch []byte, done func(kept bool))
}

// NewFlusher becomes c's OnEmit. post sends a batch off the event loop and
// calls done with whether the node kept it.
func NewFlusher(c *Client, now func() time.Time, schedule debounce.Schedule, post func(batch []byte, done func(kept bool))) *Flusher {
	f := &Flusher{c: c, pump: NewPump(now()), now: now, post: post}
	clock := func() float64 { return float64(now().UnixMilli()) }
	f.timer = debounce.New(schedule, clock, cadence.TraceFlushMode, f.Kick)
	c.OnEmit = f.Kick
	return f
}

// Kick posts if a post is due, and otherwise arms the timer for when one
// will be. It runs after an emit, on the timer, and when a post is answered.
func (f *Flusher) Kick() {
	now := f.now()
	if batch, done := f.pump.Start(f.c, now); batch != nil {
		f.post(batch, f.answered(done))
		return
	}
	if wait, ok := f.pump.Wait(f.c, now); ok {
		f.timer.Arm(int((wait + time.Millisecond - 1) / time.Millisecond))
	}
}

// Force is Pump.Force for the dump, whose answer kicks the loop like any
// other post's.
func (f *Flusher) Force() (batch []byte, done func(kept bool)) {
	b, d := f.pump.Force(f.c, f.now())
	if b == nil {
		return nil, nil
	}
	return b, f.answered(d)
}

func (f *Flusher) answered(done func(bool)) func(bool) {
	return func(kept bool) {
		done(kept)
		f.Kick()
	}
}
