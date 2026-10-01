// Package retry owns the client's retry cadences: how long the shim waits
// before asking again.
package retry

import (
	"sync"
	"time"
)

// Backstop is how often the client re-posts what the outbox still holds
// without waiting for a reconnect; see docs/freshness.md layer 7.
const Backstop = 30 * time.Second

const (
	// SubscribeRetry and StreamEndPause are flat: everything on screen goes
	// stale while the client is off the stream, so a backoff would grow the gap.
	SubscribeRetry = time.Second
	StreamEndPause = 500 * time.Millisecond
)

const (
	// HandshakeFirst and HandshakeMax bound the boot handshake's backoff; boot
	// has nothing on screen to go stale.
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

// Interval is a wait a Set restarts on the new value, so a test can lower a
// cadence it is about to bound (client/wasm/testhook.go, setBackstopMs).
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

// Reconnect paces the client's event stream and remembers whether a gap
// swallowed events: Subscribe carries no cursor, so the next stream after any
// break owes one resync kick. See docs/freshness.md layer 7.
type Reconnect struct {
	gap bool
}

// SubscribeFailed is the wait before asking again.
func (r *Reconnect) SubscribeFailed() time.Duration {
	r.gap = true
	return SubscribeRetry
}

// Subscribed reports whether this stream owes a resync kick: once per gap,
// and never for the first stream, which missed nothing.
func (r *Reconnect) Subscribed() bool {
	kick := r.gap
	r.gap = false
	return kick
}

// StreamEnded is the pause before re-subscribing.
func (r *Reconnect) StreamEnded() time.Duration {
	r.gap = true
	return StreamEndPause
}
