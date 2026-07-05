package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// flakyWatchPlugin is a plugin whose Info always succeeds and declares
// Watch: true, but whose Subscribe stream fails its first failSubFirstN
// calls before settling into a healthy (never-sending, context-lived)
// stream. It is the seam-level fake for fanInEvents' down/recovery
// transition — a unit test on fanInEvents in isolation would not prove the
// transition reaches a real client stream over the real wire; this does.
type flakyWatchPlugin struct {
	pb.UnimplementedGridwellServer
	subCalls      atomic.Int32
	failSubFirstN int32
}

func (p *flakyWatchPlugin) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return &pb.InfoResponse{Kind: "test", DisplayName: "T", RootGridId: "1", Watch: true}, nil
}

func (p *flakyWatchPlugin) Subscribe(_ *pb.SubscribeRequest, stream grpc.ServerStreamingServer[pb.Event]) error {
	n := p.subCalls.Add(1)
	if n <= p.failSubFirstN {
		return errors.New("simulated plugin stream failure")
	}
	<-stream.Context().Done()
	return nil
}

// recvHealth reads events off stream until it sees an EventPluginHealth (skip
// any grid/tile events, though none are expected here) or the deadline hits.
func recvHealth(t *testing.T, stream *rpc.EventStream) *rpc.PluginHealth {
	t.Helper()
	for {
		ev, ok, err := stream.Recv()
		if err != nil {
			t.Fatalf("stream.Recv: %v", err)
		}
		if !ok {
			t.Fatal("stream ended before a health event arrived")
		}
		if ev.Kind == rpc.EventPluginHealth {
			return ev.PluginHealth
		}
	}
}

// TestSubscribeFanInReportsHealthDownAndRecovery kills a plugin's event
// stream once and asserts the client's Subscribe stream receives an
// EventPluginHealth(healthy=false) transition followed by
// EventPluginHealth(healthy=true) on the retry that succeeds — proving
// fanInEvents' backoff loop (issue #47) tells the client about the outage
// instead of the client silently going stale with tiles that stop updating.
func TestSubscribeFanInReportsHealthDownAndRecovery(t *testing.T) {
	fake := &flakyWatchPlugin{failSubFirstN: 1}
	client, closer, err := plugin.ServeInProcess(fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register("u-1", "test", client, nil)
	srv := New(reg, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
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
	if down.PluginUUID != "u-1" {
		t.Errorf("plugin uuid = %q, want u-1", down.PluginUUID)
	}
	if down.Detail == "" {
		t.Error("down transition must carry the underlying failure as Detail")
	}

	up := recvHealth(t, stream)
	if !up.Healthy {
		t.Error("second health event must report healthy=true (recovery)")
	}
	if up.PluginUUID != "u-1" {
		t.Errorf("plugin uuid = %q, want u-1", up.PluginUUID)
	}
}

// alwaysFailInfoWatchPlugin fails Info on its first failInfoFirstN calls,
// then succeeds with Watch: true. Models the bug this test guards against:
// before the fix, a single failed Info AT SUBSCRIBE TIME permanently excluded
// the plugin from that stream's fan-in — retrying never happened.
type alwaysFailInfoWatchPlugin struct {
	pb.UnimplementedGridwellServer
	infoCalls      atomic.Int32
	failInfoFirstN int32
}

func (p *alwaysFailInfoWatchPlugin) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	n := p.infoCalls.Add(1)
	if n <= p.failInfoFirstN {
		return nil, errors.New("simulated info failure")
	}
	return &pb.InfoResponse{Kind: "test", DisplayName: "T", RootGridId: "1", Watch: true}, nil
}

func (p *alwaysFailInfoWatchPlugin) Subscribe(_ *pb.SubscribeRequest, stream grpc.ServerStreamingServer[pb.Event]) error {
	<-stream.Context().Done()
	return nil
}

// TestSubscribeRetriesInfoFailureInsteadOfPermanentlyExcluding is the
// regression test for the second bug in issue #47: an Info failure at
// Subscribe time must not permanently drop a plugin's fan-in for the life of
// the client stream. Before the fix, Subscribe's Info fetch happened once,
// synchronously, before launching fanInEvents at all; a failure there meant
// `continue` and the plugin's fan-in goroutine was never started at all — no
// amount of waiting recovered it. Now watchPlugin retries Info with backoff,
// so a plugin that is merely slow to come up still gets its events fanned in
// once Info succeeds — observable here as a health-down event (Info failed)
// followed by a health-recovery event (the retried Info succeeded).
func TestSubscribeRetriesInfoFailureInsteadOfPermanentlyExcluding(t *testing.T) {
	fake := &alwaysFailInfoWatchPlugin{failInfoFirstN: 1}
	client, closer, err := plugin.ServeInProcess(fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register("u-2", "test", client, nil)
	srv := New(reg, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
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

// noWatchAfterInfoFailPlugin fails Info once, then succeeds with Watch: false
// (the fs/proc shape — no event stream to fan in).
type noWatchAfterInfoFailPlugin struct {
	pb.UnimplementedGridwellServer
	infoCalls atomic.Int32
}

func (p *noWatchAfterInfoFailPlugin) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	if p.infoCalls.Add(1) == 1 {
		return nil, errors.New("simulated info failure")
	}
	return &pb.InfoResponse{Kind: "fs", DisplayName: "F", RootGridId: "1", Watch: false}, nil
}

// TestWatchPluginResolvesHealthBeforeNoWatchReturn: a plugin that reported
// health-down during a transient Info failure and then recovers as a
// Watch:false plugin (fs/proc) must still emit the recovery event. If the
// !info.Watch early-return runs before the recovery report, the client keeps
// a stale "live updates stopped" notice forever for a plugin that never had
// live updates to begin with.
func TestWatchPluginResolvesHealthBeforeNoWatchReturn(t *testing.T) {
	fake := &noWatchAfterInfoFailPlugin{}
	client, closer, err := plugin.ServeInProcess(fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register("u-3", "fs", client, nil)
	srv := New(reg, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
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
		t.Fatalf("first health event = %+v, want down (the transient Info failure)", down)
	}
	up := recvHealth(t, stream)
	if !up.Healthy {
		t.Fatalf("second health event = %+v, want recovery even though Watch=false", up)
	}
}
