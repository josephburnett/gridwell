package retry

import (
	"context"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Stream is one open event stream (rpc.EventStream).
type Stream interface {
	// Recv returns (nil, false, nil) at a clean end.
	Recv() (*pb.Event, bool, error)
	Close() error
}

// Events keeps the client's one event stream open until ctx ends, re-opening
// it on Reconnect's cadence.
type Events struct {
	Subscribe func(context.Context) (Stream, error)
	// Opened runs once per stream that opens, before its first event; resync
	// is Reconnect.Subscribed.
	Opened func(resync bool)
	// Ended runs once per stream that opened, when it ends: err is nil for a
	// clean end, which is a gap like a break.
	Ended func(err error)
	// Broken runs when a subscribe or an open stream fails.
	Broken func()
	Event  func(*pb.Event)
}

// Run follows the stream until ctx ends.
func (e Events) Run(ctx context.Context) {
	var pace Reconnect
	for ctx.Err() == nil {
		stream, err := e.Subscribe(ctx)
		if err != nil {
			e.Broken()
			sleep(ctx, pace.SubscribeFailed())
			continue
		}
		e.Opened(pace.Subscribed())
		for {
			ev, ok, err := stream.Recv()
			if err != nil {
				e.Ended(err)
				e.Broken()
				break
			}
			if !ok {
				e.Ended(nil)
				break
			}
			e.Event(ev)
		}
		_ = stream.Close()
		sleep(ctx, pace.StreamEnded())
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
