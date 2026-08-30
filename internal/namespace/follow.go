package namespace

// Following a namespace's event stream.
//
// In process there is no stream open to observe: Subscribe is one call that
// runs for the stream's whole life. Follow supplies the missing moment,
// once, so the node's fan-in (internal/server) and the transport's
// (internal/remote) agree on what established means instead of each
// inventing a rule.

import (
	"context"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// SettleTime is how long a Subscribe must run without failing before it
// counts as established, absent a first event. Short enough that a user
// waiting on a recovery notice sees it promptly; long enough that a
// namespace failing immediately, which is what "still down" looks like,
// never reports itself healthy between retries.
const SettleTime = 250 * time.Millisecond

// Follow runs ns.Subscribe, relaying events to onEvent, and calls established
// exactly once: on the first event, or after SettleTime of a call that has
// not failed, whichever comes first. A stream that fails before either never
// calls it. Returns when the subscription ends.
func Follow(ctx context.Context, ns Namespace, req *pb.SubscribeRequest, onEvent func(*pb.Event) error, established func()) error {
	firstEvent := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		seen := false
		done <- ns.Subscribe(ctx, req, func(ev *pb.Event) error {
			if !seen {
				seen = true
				firstEvent <- struct{}{}
			}
			return onEvent(ev)
		})
	}()
	settle := time.NewTimer(SettleTime)
	defer settle.Stop()
	select {
	case err := <-done:
		return err // ended before it ever proved itself: never established
	case <-firstEvent:
	case <-settle.C:
	}
	established()
	return <-done
}
