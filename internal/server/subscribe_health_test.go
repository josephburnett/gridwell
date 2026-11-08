package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/namespace/namespacetest"
)

// healthNode is the node these fakes are the files of.
const healthNode = "lnode1"

// flakyWatch is a namespace whose Info always succeeds and whose Subscribe
// stream fails its first failSubFirstN calls before settling into a healthy
// stream that sends its header and nothing else until its context ends. It is the seam-level fake for the fan-in's down/recovery
// transition — a unit test on the loop in isolation would not prove the
// transition reaches a real client stream over the real wire; this does.
type flakyWatch struct {
	namespacetest.Unimplemented
	subCalls      atomic.Int32
	failSubFirstN int32
}

func (p *flakyWatch) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return &pb.InfoResponse{DisplayName: "T", RootGridId: "1"}, nil
}

func (p *flakyWatch) Subscribe(ctx context.Context, _ *pb.SubscribeRequest, send func(*pb.Event) error) error {
	n := p.subCalls.Add(1)
	if n <= p.failSubFirstN {
		return errors.New("simulated stream failure")
	}
	if err := send(nil); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// recvHealth reads events off stream until it sees an EventSourceHealth (skip
// any grid/tile events, though none are expected here) or the deadline hits.
func recvHealth(t *testing.T, stream *rpc.EventStream) *pb.EventSourceHealth {
	t.Helper()
	for {
		ev, ok, err := stream.Recv()
		if err != nil {
			t.Fatalf("stream.Recv: %v", err)
		}
		if !ok {
			t.Fatal("stream ended before a health event arrived")
		}
		if h := ev.GetSourceHealth(); h != nil {
			return h
		}
	}
}

// TestSubscribeFanInReportsHealthDownAndRecovery kills a namespace's event
// stream once and asserts the client's Subscribe stream receives an
// EventSourceHealth(healthy=false) transition followed by
// EventSourceHealth(healthy=true) on the retry that succeeds — proving
// the fan-in's backoff loop tells the client about the outage
// instead of the client silently going stale with tiles that stop updating.
func TestSubscribeFanInReportsHealthDownAndRecovery(t *testing.T) {
	fake := &flakyWatch{failSubFirstN: 1}
	client := fake
	reg := &Sources{}
	reg.Files = client
	srv := mustNew(t, reg, Config{ID: healthNode})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	stream, err := cl.Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer stream.Close()

	down := recvHealth(t, stream)
	if down.Healthy {
		t.Error("first health event must report healthy=false (the down transition)")
	}
	if down.Uuid != rpc.FilesOwner(healthNode) {
		t.Errorf("health uuid = %q, want %s", down.Uuid, rpc.FilesOwner(healthNode))
	}
	if down.Detail == "" {
		t.Error("down transition must carry the underlying failure as Detail")
	}

	up := recvHealth(t, stream)
	if !up.Healthy {
		t.Error("second health event must report healthy=true (recovery)")
	}
	if up.Uuid != rpc.FilesOwner(healthNode) {
		t.Errorf("health uuid = %q, want %s", up.Uuid, rpc.FilesOwner(healthNode))
	}
}

// failInfoWatch fails Info on its first failInfoFirstN calls,
// then succeeds. Models the bug this test guards against:
// before the fix, a single failed Info AT SUBSCRIBE TIME permanently excluded
// the namespace from that stream's fan-in — retrying never happened.
type failInfoWatch struct {
	namespacetest.Unimplemented
	infoCalls      atomic.Int32
	failInfoFirstN int32
}

func (p *failInfoWatch) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	n := p.infoCalls.Add(1)
	if n <= p.failInfoFirstN {
		return nil, errors.New("simulated info failure")
	}
	return &pb.InfoResponse{DisplayName: "T", RootGridId: "1"}, nil
}

func (p *failInfoWatch) Subscribe(ctx context.Context, _ *pb.SubscribeRequest, send func(*pb.Event) error) error {
	if err := send(nil); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// An Info failure at Subscribe time must not permanently drop a namespace's
// fan-in for the life of the client stream: watchSource retries Info with
// backoff, so a namespace merely slow to answer still gets its events fanned in,
// observable here as a health-down event followed by a health-recovery one.
func TestSubscribeRetriesInfoFailureInsteadOfPermanentlyExcluding(t *testing.T) {
	fake := &failInfoWatch{failInfoFirstN: 1}
	client := fake
	reg := &Sources{}
	reg.Files = client
	srv := mustNew(t, reg, Config{ID: healthNode})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	stream, err := cl.Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer stream.Close()

	down := recvHealth(t, stream)
	if down.Healthy {
		t.Fatalf("first health event = %+v, want a health-down event (the Info failure)", down)
	}

	up := recvHealth(t, stream)
	if !up.Healthy {
		t.Fatalf("second health event = %+v, want a health-recovery event (Info retry succeeded)", up)
	}
	if got := fake.infoCalls.Load(); got < 2 {
		t.Errorf("Info called %d times, want at least 2 (fail, then a retried success) — the permanent-exclusion bug never retries", got)
	}
}

// The fan-in's waiting is namespace.DefaultBackoff and nothing of its own:
// lower the policy and four dead re-dials pass in milliseconds, where the
// declared 1s doubling would spend fifteen seconds on them. Without this the
// numbers in the loop answer to no test.
func TestSubscribeFanInRetriesOnTheDeclaredBackoff(t *testing.T) {
	was := namespace.DefaultBackoff
	t.Cleanup(func() { namespace.DefaultBackoff = was })
	namespace.DefaultBackoff = namespace.Backoff{First: 10 * time.Millisecond, Max: 20 * time.Millisecond}

	fake := &flakyWatch{failSubFirstN: 4}
	reg := &Sources{}
	reg.Files = fake
	srv := mustNew(t, reg, Config{ID: healthNode})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	stream, err := cl.Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer stream.Close()

	start := time.Now()
	if down := recvHealth(t, stream); down.Healthy {
		t.Fatalf("first health event = %+v, want the down transition", down)
	}
	if up := recvHealth(t, stream); !up.Healthy {
		t.Fatalf("second health event = %+v, want recovery", up)
	}
	// Generous against a loaded machine, and still far under the 15s the
	// declared policy would take for four failures.
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("recovery took %v: the fan-in is waiting on numbers of its own, not namespace.DefaultBackoff", took)
	}
}
