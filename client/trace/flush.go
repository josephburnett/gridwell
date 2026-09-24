package trace

import (
	"time"

	"github.com/josephburnett/gridwell/client/cadence"
)

// FlushBatch is the count that posts a batch ahead of the clock. A burst of
// records is a gesture going wrong, which is the moment the node most needs
// to hear, and the batch stays small enough for one body.
const FlushBatch = 200

// NeedFlush is the one flush decision: the shim asks it on its timer and
// after an emit. Nothing owed is nothing to post, so an idle client costs no
// round trips.
func NeedFlush(pendingCount int, sinceLast time.Duration) bool {
	if pendingCount <= 0 {
		return false
	}
	return pendingCount >= FlushBatch || sinceLast >= cadence.TraceFlushMs*time.Millisecond
}
