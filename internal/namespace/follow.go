package namespace

// Follow supplies the moment an in-process subscription counts as established.
// Subscribe is one call that runs for the stream's whole life, so the header is
// the only open stream there is to observe, and deciding it here keeps
// internal/server and internal/connection agreed on what established means.

import (
	"context"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Follow calls established exactly once, on the stream's header or first
// event, and never by time: a stream that has said nothing has not said it is
// listening, and a change made before it is would be missed. A stream that
// fails or never speaks never calls it. The header is not an event, so onEvent
// never sees it.
func Follow(ctx context.Context, ns Namespace, req *pb.SubscribeRequest, onEvent func(*pb.Event) error, established func()) error {
	seen := false
	return ns.Subscribe(ctx, req, func(ev *pb.Event) error {
		if !seen {
			seen = true
			established()
		}
		if ev == nil {
			return nil
		}
		return onEvent(ev)
	})
}
