package trace

import (
	"time"

	"github.com/josephburnett/gridwell/client/cadence"
)

// Pump is the flush loop's own state: when the last post started and whether
// one is still out. One at a time, because a second would carry the records
// the first is already carrying and the node would hold both.
type Pump struct {
	inFlight bool
	failed   bool
	last     time.Time
}

func NewPump(now time.Time) *Pump { return &Pump{last: now} }

// Start hands over the batch to post and the completion to call with the
// node's answer, or nil when nothing is due yet. A failure acknowledges
// nothing, so those records ride the next batch.
func (p *Pump) Start(c *Client, now time.Time) (batch []byte, done func(kept bool)) {
	if p.inFlight || !p.due(c.PendingCount(), now) {
		return nil, nil
	}
	return p.force(c, now)
}

// force takes the batch with no due check of its own.
func (p *Pump) force(c *Client, now time.Time) ([]byte, func(bool)) {
	b, ack := c.PendingBatch()
	if len(b) == 0 {
		return nil, nil
	}
	p.inFlight = true
	p.last = now
	return b, func(kept bool) {
		p.inFlight = false
		p.failed = !kept
		if kept {
			ack()
		}
	}
}

// due is NeedFlush plus the one rule the count alone gets wrong: after a post
// the node did not keep, the next one waits for the clock. The failure is
// itself a record, so a full ring would otherwise post, fail, grow by the
// failure, and post again as fast as the door can refuse.
func (p *Pump) due(pending int, now time.Time) bool {
	since := now.Sub(p.last)
	if p.failed && since < cadence.TraceFlushMs*time.Millisecond {
		return false
	}
	return NeedFlush(pending, since)
}
