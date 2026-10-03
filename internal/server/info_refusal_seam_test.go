package server_test

// A plugin that cannot serve its declared source refuses Info with the
// reason. Across the plugin.v1 wire, the adapter and the web door, the node
// lists it broken with that sentence and none of its entries, tells a
// subscribed client so, and asks again until the plugin answers, when the
// entries come back without a restart.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

const refusingUUID = "prefuse"

const refusal = `root "/srv/missing" does not exist`

// refusingInfo refuses Info while fixed is false, as fs does for a root that
// is not there, and declares one collection once it answers.
type refusingInfo struct {
	pluginv1.UnimplementedPluginServer
	fixed *atomic.Bool
}

func (p refusingInfo) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	if !p.fixed.Load() {
		return nil, status.Error(codes.FailedPrecondition, refusal)
	}
	return &pluginv1.InfoResponse{Kind: "fs", DisplayName: "missing",
		MenuEntries: []*pluginv1.MenuEntry{{Id: ".", Context: "."}}}, nil
}

func pluginRow(t *testing.T, cl *rpc.Client, uuid string) *gridwellv1.PluginInfo {
	t.Helper()
	lp, err := cl.Handshake(context.Background())
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	for _, p := range lp.Plugins {
		if p.Uuid == uuid {
			return p
		}
	}
	t.Fatalf("plugin %s is not listed: %v", uuid, lp.Plugins)
	return nil
}

func TestARefusedInfoIsBrokenWithItsReasonAndComesBack(t *testing.T) {
	was := namespace.DefaultBackoff
	t.Cleanup(func() { namespace.DefaultBackoff = was })
	namespace.DefaultBackoff = namespace.Backoff{First: 10 * time.Millisecond, Max: 20 * time.Millisecond}

	st, err := store.Open(t.TempDir() + "/gridwell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var fixed atomic.Bool
	cp, closer, err := plugintest.Loopback(refusingInfo{fixed: &fixed})
	if err != nil {
		t.Fatal(err)
	}
	a, stop := pluginhost.Start(cp, st.Namespace(refusingUUID), nil, "plugin "+refusingUUID+" watch")
	reg := plugin.NewRegistry()
	reg.Register(refusingUUID, "fs", a, func() { stop(); closer() })
	reg.SetLabel(refusingUUID, "docs")
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	row := pluginRow(t, cl, refusingUUID)
	if row.InfoError != refusal {
		t.Errorf("InfoError = %q, want the plugin's own sentence %q", row.InfoError, refusal)
	}
	if len(row.MenuEntries) != 0 {
		t.Errorf("a refusing plugin presents entries: %v", row.MenuEntries)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := cl.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	down := recvHealthOf(t, stream, refusingUUID)
	if down.Healthy {
		t.Fatalf("first health event = %+v, want the refusal as down", down)
	}

	fixed.Store(true)
	if up := recvHealthOf(t, stream, refusingUUID); !up.Healthy {
		t.Fatalf("after the fix, health event = %+v, want up", up)
	}
	row = pluginRow(t, cl, refusingUUID)
	if row.InfoError != "" || len(row.MenuEntries) != 1 {
		t.Errorf("after the fix the row is %v, want its one entry and no error", row)
	}
}

func recvHealthOf(t *testing.T, stream *rpc.EventStream, uuid string) *gridwellv1.EventPluginHealth {
	t.Helper()
	for {
		ev, ok, err := stream.Recv()
		if err != nil || !ok {
			t.Fatalf("the stream ended before a health event for %s: %v", uuid, err)
		}
		if h := ev.GetPluginHealth(); h != nil && h.PluginUuid == uuid {
			return h
		}
	}
}
