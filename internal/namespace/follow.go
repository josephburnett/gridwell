package namespace

// Follow supplies the moment an in-process subscription counts as established.
// Subscribe is one call that runs for the stream's whole life, so unless the
// namespace sends its header there is no open stream to observe, and deciding
// it here keeps internal/server and internal/connection agreed on what
// established means.

import (
	"context"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// SettleTime is how long a Subscribe must run without failing to count as
// established, absent a first event: short enough that a recovery notice
// arrives promptly, long enough that a namespace failing immediately never
// reports itself healthy between retries.
const SettleTime = 250 * time.Millisecond

// Follow calls established exactly once: on the stream's header or first
// event, or after SettleTime of a call that has not failed. A stream that
// fails before any of them never calls it. The header is not an event, so
// onEvent never sees it.
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
			if ev == nil {
				return nil
			}
			return onEvent(ev)
		})
	}()
	settle := time.NewTimer(SettleTime)
	defer settle.Stop()
	select {
	case err := <-done:
		return err // never established
	case <-firstEvent:
	case <-settle.C:
	}
	established()
	return <-done
}
