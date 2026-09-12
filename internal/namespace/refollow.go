package namespace

// Refollow is the loop around Follow: the stream ended, wait, dial it again,
// and say so. The node's plugin fan-in and a connection's fan-in are its two
// callers, so what "waiting" and "saying so" mean is decided once here rather
// than spelled out again on each side.

import (
	"context"
	"errors"
	"log"
	"time"
)

// Backoff is how long a fan-in waits before re-dialing: First after the stream
// ends, doubling to Max, back to First the moment one establishes.
type Backoff struct {
	First time.Duration
	Max   time.Duration
}

// DefaultBackoff is the policy both fan-ins run on. Only tests give a
// Refollow a different one.
var DefaultBackoff = Backoff{First: time.Second, Max: 30 * time.Second}

// ErrStreamEnded stands in for a stream that ended without an error, because
// events stopping is an outage either way.
var ErrStreamEnded = errors.New("the event stream ended")

// Refollow re-dials one namespace's event stream until its context ends.
type Refollow struct {
	// Label names the stream in the retry log, as "connection rtb".
	Label string
	// Attempt follows the stream once and returns when it ends. It calls
	// established when the stream counts as live — Follow decides that
	// moment — from its own goroutine, which is the only one Refollow's
	// state is touched on.
	Attempt func(ctx context.Context, established func()) error
	// Down is called with the reason on the first failure of an outage, and
	// Up on the establish that ends it: once each, never once per retry.
	Down func(detail string)
	Up   func()
	// Backoff is DefaultBackoff when left zero.
	Backoff Backoff
}

// Run follows until ctx ends. A stream that has not failed yet is healthy, so
// a caller that already reported its own recovery is not made to report a
// second one.
func (r Refollow) Run(ctx context.Context) {
	b := r.Backoff
	if b == (Backoff{}) {
		b = DefaultBackoff
	}
	wait := b.First
	healthy := true
	for {
		err := r.Attempt(ctx, func() {
			wait = b.First
			if !healthy {
				healthy = true
				r.Up()
			}
		})
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = ErrStreamEnded
		}
		log.Printf("gridwell: %s: %v — retrying in %v", r.Label, err, wait)
		if healthy {
			healthy = false
			r.Down(err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait *= 2
		if wait > b.Max {
			wait = b.Max
		}
	}
}
