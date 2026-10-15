package namespace

// A stream is established by its header or first event and by nothing else:
// not by time, and never after it fails.

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

// quiet is how long a test watches for something that must not happen.
const quiet = 500 * time.Millisecond

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
	case <-time.After(quiet):
		t.Fatal("Follow went on waiting on a call that had already failed")
	}
	select {
	case at := <-est:
		t.Fatalf("established after %v — a stream that failed before it spoke never establishes", at)
	default:
	}
}

// A first event establishes: a codec whose header rides its first message
// sends no separate one.
func TestFollowEstablishesOnTheFirstEvent(t *testing.T) {
	est, _ := follow(t, followNS{run: func(ctx context.Context, send func(*pb.Event) error) error {
		if err := send(&pb.Event{}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}})

	select {
	case <-est:
	case <-time.After(10 * quiet):
		t.Fatal("an event arrived and the subscription never established")
	}
}

// A stream that has said nothing has not said it is listening, however long
// it runs: a far node that never sends its header never reads as up.
func TestFollowNeverEstablishesASilentStream(t *testing.T) {
	est, _ := follow(t, followNS{run: func(ctx context.Context, _ func(*pb.Event) error) error {
		<-ctx.Done()
		return ctx.Err()
	}})

	select {
	case at := <-est:
		t.Fatalf("established after %v on a stream that sent neither a header nor an event", at)
	case <-time.After(quiet):
	}
}

// A header establishes, as a first event does, and is not an event.
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
	case <-est:
	case <-time.After(10 * quiet):
		t.Fatal("a header arrived and the subscription never established")
	}
	select {
	case ev := <-heard:
		t.Fatalf("the header reached onEvent as %v", ev)
	case <-time.After(quiet):
	}
}
