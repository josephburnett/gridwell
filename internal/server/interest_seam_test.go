package server_test

// Interest through the web door: what each client says it shows reaches each
// namespace as its share of the union, held while that client's event stream
// is open.

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// interestSink is a namespace that records the last share it was handed.
type interestSink struct {
	namespace.Unimplemented
	mu    sync.Mutex
	share []string
	heard int
}

func (s *interestSink) Info(context.Context, *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return &gridwellv1.InfoResponse{}, nil
}

func (s *interestSink) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, _ func(*gridwellv1.Event) error) error {
	<-ctx.Done()
	return nil
}

func (s *interestSink) SetInterest(_ context.Context, req *gridwellv1.SetInterestRequest) (*gridwellv1.SetInterestResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.share, s.heard = slices.Clone(req.GridIds), s.heard+1
	return &gridwellv1.SetInterestResponse{}, nil
}

// await waits for the sink's share to be want.
func (s *interestSink) await(t *testing.T, want []string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		got, heard := slices.Clone(s.share), s.heard
		s.mu.Unlock()
		if heard > 0 && slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("share = %v (heard %d), want %v", got, heard, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// subscribed opens cl's event stream and returns its close. The open is not
// awaited: a stream with nothing to say has not answered yet, and a set sent
// before the node sees the stream is held until it does.
func subscribed(cl *rpc.Client) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		es, err := cl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer es.Close()
		for {
			if _, ok, err := es.Recv(); err != nil || !ok {
				return
			}
		}
	}()
	return cancel
}

// Two clients showing overlapping grids of one namespace hand it the union,
// peeled to its own ids, and a grid of another namespace is not its business;
// a client whose stream closes takes its grids with it.
func TestInterestReachesEachNamespaceAsItsShare(t *testing.T) {
	const a, b = "pinta01", "pintb01"
	sinkA, sinkB := &interestSink{}, &interestSink{}
	reg := plugin.NewRegistry()
	reg.Register(a, "feed", sinkA, nil)
	reg.Register(b, "feed", sinkB, nil)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	one := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	two := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx := context.Background()

	closeOne := subscribed(one)
	closeTwo := subscribed(two)
	if err := one.SetInterest(ctx, []string{a + "/1", a + "/2", b + "/9"}); err != nil {
		t.Fatal(err)
	}
	if err := two.SetInterest(ctx, []string{a + "/2", a + "/3"}); err != nil {
		t.Fatal(err)
	}
	sinkA.await(t, []string{"1", "2", "3"})
	sinkB.await(t, []string{"9"})

	closeOne()
	sinkA.await(t, []string{"2", "3"})
	sinkB.await(t, nil)
	closeTwo()
	sinkA.await(t, nil)
}
