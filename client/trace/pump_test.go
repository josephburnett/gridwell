package trace

import (
	"strings"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/client/cadence"
)

var flushWindow = cadence.TraceFlushMs * time.Millisecond

func pending(c *Client, n int, at time.Time) {
	for i := 0; i < n; i++ {
		c.Emit("nav", "push", "x", nil, at)
	}
}

// A post the node kept acknowledges its records; the next tick has nothing to
// say and costs no round trip.
func TestAKeptPostAcknowledgesAndThenIdles(t *testing.T) {
	c, p := New(4096, "cid7abc"), NewPump(t0)
	pending(c, 1, t0)
	batch, done := p.Start(c, t0.Add(flushWindow))
	if batch == nil {
		t.Fatal("a record older than the window is not due")
	}
	done(true)
	if b, _ := p.Start(c, t0.Add(2*flushWindow)); b != nil {
		t.Errorf("an acknowledged ring still posts %q", b)
	}
}

// Only one post is out at a time: a second would carry the same records.
func TestOnlyOnePostIsInFlight(t *testing.T) {
	c, p := New(4096, "cid7abc"), NewPump(t0)
	pending(c, 1, t0)
	batch, done := p.Start(c, t0.Add(flushWindow))
	if batch == nil {
		t.Fatal("nothing was due")
	}
	pending(c, FlushBatch, t0)
	if b, _ := p.Start(c, t0.Add(2*flushWindow)); b != nil {
		t.Errorf("a second post started while one was out: %q", b)
	}
	done(true)
	if b, _ := p.Start(c, t0.Add(2*flushWindow)); b == nil {
		t.Error("the records emitted in flight were never posted")
	}
}

// A burst posts ahead of the clock, which is the moment the node most needs
// to hear.
func TestABurstPostsBeforeTheWindowCloses(t *testing.T) {
	c, p := New(4096, "cid7abc"), NewPump(t0)
	pending(c, FlushBatch-1, t0)
	if b, _ := p.Start(c, t0); b != nil {
		t.Errorf("a short batch posted inside the window: %d records", strings.Count(string(b), "\n"))
	}
	pending(c, 1, t0)
	if b, _ := p.Start(c, t0); b == nil {
		t.Error("a full batch waited for the clock")
	}
}

// The failure record must not be what starts the next post, or an unreachable
// node with a full ring is posted to as fast as the door can refuse.
func TestAFailedPostWaitsForTheClockHoweverFullTheRingIs(t *testing.T) {
	c, p := New(4096, "cid7abc"), NewPump(t0)
	pending(c, FlushBatch, t0)
	batch, done := p.Start(c, t0)
	if batch == nil {
		t.Fatal("a full batch did not post")
	}
	done(false)
	// The post failed, so the batch is still owed, and the failure itself is
	// one more record.
	c.Emit("trace", "flush", "post failed", nil, t0)
	if b, _ := p.Start(c, t0.Add(time.Millisecond)); b != nil {
		t.Error("a failed post retried immediately on the count alone")
	}
	b, _ := p.Start(c, t0.Add(flushWindow))
	if b == nil {
		t.Fatal("the retry never came")
	}
	if !strings.Contains(string(b), "post failed") {
		t.Error("the retry dropped the records the failed post carried")
	}
}
