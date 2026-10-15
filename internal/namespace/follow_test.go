package namespace

// SettleTime is a promise about time, so every wait here is derived from it:
// a first event establishes before it elapses, a silent stream establishes
// when it does, and a stream that fails first never establishes at all.

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// followNS is a Subscribe the test drives.
type followNS struct {
	Unimplemented
	run func(ctx context.Context, send func(*pb.Event) error) error
}

func (n followNS) Subscribe(ctx context.Context, _ *pb.SubscribeRequest, send func(*pb.Event) error) error {
	return n.run(ctx, send)
}

var errSubscribeFailed = errors.New("the namespace refused the subscription")

// follow runs Follow in the background and reports when established was called.
// The stream's context ends with the test.
func follow(t *testing.T, ns Namespace) (established <-chan time.Duration, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	est := make(chan time.Duration, 1)
	errc := make(chan error, 1)
	start := time.Now()
	go func() {
		errc <- Follow(ctx, ns, &pb.SubscribeRequest{}, func(*pb.Event) error { return nil },
			func() { est <- time.Since(start) })
	}()
	return est, errc
}

// A subscription that fails before it settles never counted as established, so
// nothing may report itself healthy on the strength of a call that failed.
func TestFollowNeverEstablishesAStreamThatFailsFirst(t *testing.T) {
	est, done := follow(t, followNS{run: func(context.Context, func(*pb.Event) error) error {
		return errSubscribeFailed
	}})

	select {
	case err := <-done:
		if !errors.Is(err, errSubscribeFailed) {
			t.Fatalf("Follow = %v, want the subscription's own error", err)
		}
	case <-time.After(SettleTime):
		t.Fatal("Follow waited out SettleTime on a call that had already failed")
	}
	// The timer is still running here; the failed stream must not establish
	// when it fires.
	select {
	case at := <-est:
		t.Fatalf("established after %v — a stream that failed before settling never establishes", at)
	case <-time.After(2 * SettleTime):
	}
}

// A first event establishes at once: SettleTime is the floor for a silent
// stream, never a delay imposed on one that has already spoken.
func TestFollowEstablishesOnTheFirstEvent(t *testing.T) {
	est, _ := follow(t, followNS{run: func(ctx context.Context, send func(*pb.Event) error) error {
		if err := send(&pb.Event{}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}})

	select {
	case at := <-est:
		if at >= SettleTime {
			t.Fatalf("established after %v — an event that arrived first must not wait out SettleTime (%v)", at, SettleTime)
		}
	case <-time.After(4 * SettleTime):
		t.Fatal("an event arrived and the subscription never established")
	}
}

// A silent stream that has not failed establishes at SettleTime: not before,
// or a namespace failing on the next line would report itself healthy, and not
// much after, or a recovery notice arrives late.
func TestFollowEstablishesASilentStreamAfterSettleTime(t *testing.T) {
	est, _ := follow(t, followNS{run: func(ctx context.Context, _ func(*pb.Event) error) error {
		<-ctx.Done()
		return ctx.Err()
	}})

	select {
	case at := <-est:
		if at < SettleTime {
			t.Fatalf("established after %v, before SettleTime (%v) — a stream that has said nothing has not settled", at, SettleTime)
		}
	case <-time.After(8 * SettleTime):
		t.Fatalf("a healthy silent subscription never established; SettleTime is %v", SettleTime)
	}
}

// A header establishes at once, as a first event does, and is not an event.
func TestFollowEstablishesOnTheHeaderAndDeliversNoEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ns := followNS{run: func(ctx context.Context, send func(*pb.Event) error) error {
		if err := send(nil); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	est := make(chan time.Duration, 1)
	heard := make(chan *pb.Event, 1)
	start := time.Now()
	go func() {
		_ = Follow(ctx, ns, &pb.SubscribeRequest{}, func(ev *pb.Event) error { heard <- ev; return nil },
			func() { est <- time.Since(start) })
	}()
	select {
	case at := <-est:
		if at >= SettleTime {
			t.Fatalf("established after %v — a header must not wait out SettleTime (%v)", at, SettleTime)
		}
	case <-time.After(4 * SettleTime):
		t.Fatal("a header arrived and the subscription never established")
	}
	select {
	case ev := <-heard:
		t.Fatalf("the header reached onEvent as %v", ev)
	case <-time.After(SettleTime):
	}
}
