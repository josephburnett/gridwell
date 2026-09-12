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
	"github.com/josephburnett/gridwell/internal/plugin"
)

// flakyWatchPlugin is a plugin whose Info always succeeds and whose Subscribe
// stream fails its first failSubFirstN calls before settling into a healthy
// (never-sending, context-lived) stream. It is the seam-level fake for the fan-in's down/recovery
// transition — a unit test on the loop in isolation would not prove the
// transition reaches a real client stream over the real wire; this does.
type flakyWatchPlugin struct {
	namespace.Unimplemented
	subCalls      atomic.Int32
	failSubFirstN int32
}

func (p *flakyWatchPlugin) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return &pb.InfoResponse{DisplayName: "T", RootGridId: "1"}, nil
}

func (p *flakyWatchPlugin) Subscribe(ctx context.Context, _ *pb.SubscribeRequest, _ func(*pb.Event) error) error {
	n := p.subCalls.Add(1)
	if n <= p.failSubFirstN {
		return errors.New("simulated plugin stream failure")
	}
	<-ctx.Done()
	return nil
}

// recvHealth reads events off stream until it sees an EventPluginHealth (skip
// any grid/tile events, though none are expected here) or the deadline hits.
func recvHealth(t *testing.T, stream *rpc.EventStream) *pb.EventPluginHealth {
	t.Helper()
	for {
		ev, ok, err := stream.Recv()
		if err != nil {
			t.Fatalf("stream.Recv: %v", err)
		}
		if !ok {
			t.Fatal("stream ended before a health event arrived")
		}
		if h := ev.GetPluginHealth(); h != nil {
			return h
		}
	}
}

// TestSubscribeFanInReportsHealthDownAndRecovery kills a plugin's event
// stream once and asserts the client's Subscribe stream receives an
// EventPluginHealth(healthy=false) transition followed by
// EventPluginHealth(healthy=true) on the retry that succeeds — proving
// the fan-in's backoff loop tells the client about the outage
// instead of the client silently going stale with tiles that stop updating.
func TestSubscribeFanInReportsHealthDownAndRecovery(t *testing.T) {
	fake := &flakyWatchPlugin{failSubFirstN: 1}
	client := fake
	reg := plugin.NewRegistry()
	reg.Register("u-1", "test", client, nil)
	srv := mustNew(t, reg, Config{})
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
	if down.PluginUuid != "u-1" {
		t.Errorf("plugin uuid = %q, want u-1", down.PluginUuid)
	}
	if down.Detail == "" {
		t.Error("down transition must carry the underlying failure as Detail")
	}

	up := recvHealth(t, stream)
	if !up.Healthy {
		t.Error("second health event must report healthy=true (recovery)")
	}
	if up.PluginUuid != "u-1" {
		t.Errorf("plugin uuid = %q, want u-1", up.PluginUuid)
	}
}

// alwaysFailInfoWatchPlugin fails Info on its first failInfoFirstN calls,
// then succeeds. Models the bug this test guards against:
// before the fix, a single failed Info AT SUBSCRIBE TIME permanently excluded
// the plugin from that stream's fan-in — retrying never happened.
type alwaysFailInfoWatchPlugin struct {
	namespace.Unimplemented
	infoCalls      atomic.Int32
	failInfoFirstN int32
}

func (p *alwaysFailInfoWatchPlugin) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	n := p.infoCalls.Add(1)
	if n <= p.failInfoFirstN {
		return nil, errors.New("simulated info failure")
	}
	return &pb.InfoResponse{DisplayName: "T", RootGridId: "1"}, nil
}

func (p *alwaysFailInfoWatchPlugin) Subscribe(ctx context.Context, _ *pb.SubscribeRequest, _ func(*pb.Event) error) error {
	<-ctx.Done()
	return nil
}

// An Info failure at Subscribe time must not permanently drop a plugin's
// fan-in for the life of the client stream: watchPlugin retries Info with
// backoff, so a plugin merely slow to come up still gets its events fanned in,
// observable here as a health-down event followed by a health-recovery one.
func TestSubscribeRetriesInfoFailureInsteadOfPermanentlyExcluding(t *testing.T) {
	fake := &alwaysFailInfoWatchPlugin{failInfoFirstN: 1}
	client := fake
	reg := plugin.NewRegistry()
	reg.Register("u-2", "test", client, nil)
	srv := mustNew(t, reg, Config{})
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

	fake := &flakyWatchPlugin{failSubFirstN: 4}
	reg := plugin.NewRegistry()
	reg.Register("u-4", "test", fake, nil)
	srv := mustNew(t, reg, Config{})
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
