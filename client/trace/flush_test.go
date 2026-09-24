package trace

import (
	"testing"
	"time"

	"github.com/josephburnett/gridwell/client/cadence"
)

// One row per reason to post, and per reason not to. An idle client posting
// on the timer would cost a round trip a second for the life of the page, and
// a burst that waits out the timer is the gesture the trace was opened for.
func TestNeedFlush(t *testing.T) {
	flushWait := cadence.TraceFlushMs * time.Millisecond
	cases := []struct {
		name      string
		pending   int
		sinceLast time.Duration
		want      bool
	}{
		{"nothing pending, however long it has been", 0, time.Hour, false},
		{"one record, still inside the window", 1, flushWait - time.Millisecond, false},
		{"one record, the window elapsed", 1, flushWait, true},
		{"a full batch, whatever the clock says", FlushBatch, 0, true},
		{"just under a batch, inside the window", FlushBatch - 1, time.Millisecond, false},
		{"past a batch, inside the window", FlushBatch + 50, time.Millisecond, true},
		{"a negative count is nothing pending", -1, time.Hour, false},
	}
	for _, c := range cases {
		if got := NeedFlush(c.pending, c.sinceLast); got != c.want {
			t.Errorf("%s: NeedFlush(%d, %v) = %v, want %v", c.name, c.pending, c.sinceLast, got, c.want)
		}
	}
}
