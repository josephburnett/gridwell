// Package retry owns the client's retry cadences: how long the shim waits
// before asking again. A duration written into a sleep is a guarantee no test
// can hold, so the values live here with the types that produce them.
package retry

import (
	"sync"
	"time"
)

// Backstop is how often the client re-posts what the outbox still holds
// without waiting for a reconnect; see docs/freshness.md layer 7.
const Backstop = 30 * time.Second

const (
	// SubscribeRetry is the wait after a failed Subscribe, StreamEndPause the
	// wait after a stream ends. Flat, because everything on screen is going
	// stale while the client is off the stream, and a backoff would make the
	// gap grow with itself.
	SubscribeRetry = time.Second
	StreamEndPause = 500 * time.Millisecond
)

const (
	// HandshakeFirst and HandshakeMax bound the boot handshake's wait. Boot
	// has nothing on screen to go stale, so it backs off; the ceiling is what
	// keeps a node that answers late from leaving the page empty for minutes.
	HandshakeFirst = time.Second
	HandshakeMax   = 15 * time.Second
)

// Backoff is a doubling wait from First, never past Max.
type Backoff struct {
	First, Max time.Duration

	cur time.Duration
}

// Next is this attempt's wait, and advances.
func (b *Backoff) Next() time.Duration {
	if b.cur <= 0 {
		b.cur = b.First
	}
	if b.cur > b.Max {
		b.cur = b.Max
	}
	d := b.cur
	b.cur *= 2
	return d
}

// Interval is a wait whose length can change while it is running: a Set
// restarts the wait in flight on the new value instead of serving out the old
// one, which is what lets a test lower a cadence it is about to bound (see
// client/wasm/testhook.go, setBackstopMs).
type Interval struct {
	mu  sync.Mutex
	d   time.Duration
	set chan struct{}
}

func NewInterval(d time.Duration) *Interval {
	return &Interval{d: d, set: make(chan struct{}, 1)}
}

// Duration is the wait in effect.
func (i *Interval) Duration() time.Duration {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.d
}

// Set changes the wait, so the next tick is d from now.
func (i *Interval) Set(d time.Duration) {
	i.mu.Lock()
	i.d = d
	i.mu.Unlock()
	select {
	case i.set <- struct{}{}:
	default:
	}
}

// Wait blocks for one interval.
func (i *Interval) Wait() {
	for {
		t := time.NewTimer(i.Duration())
		select {
		case <-t.C:
			return
		case <-i.set:
			t.Stop()
		}
	}
}
