package server_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/sourcecache"
)

const refuseUUID = "prefuse"

// refusedWatch lists one entry and refuses every Watch the way the fs plugin
// does when the OS will not give it another change watch.
type refusedWatch struct{ pokedWatch }

func (refusedWatch) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	return &pluginv1.ListResponse{Authoritative: true, Entries: []*pluginv1.Entry{{Key: "k", Label: "k", Kind: rpc.KindText}}}, nil
}

func (refusedWatch) Watch(*pluginv1.WatchRequest, grpc.ServerStreamingServer[pluginv1.Change]) error {
	return status.Error(codes.ResourceExhausted, "fs plugin: the OS refused another change watch")
}

// A source that lists but cannot watch is not dark: a client across a
// connection is told once that its live updates are off, on a healthy event,
// is never told it is down, and its listing still answers.
func TestARefusedWatchIsANoticeNotDarkness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newFrontedTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil,
		func(ns namespace.Namespace) namespace.Namespace {
			cache, err := sourcecache.Open(t.TempDir() + "/cache.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cache.Close() })
			return cache.Front(ns, sourcecache.Options{})
		},
		func(reg *plugin.Registry, st *store.Store) {
			cp, closer, err := plugintest.Loopback(refusedWatch{})
			if err != nil {
				t.Fatal(err)
			}
			a, stop := pluginhost.Start(cp, st.Namespace(refuseUUID), nil, "plugin "+refuseUUID+" watch")
			reg.Register(refuseUUID, "feed", a, func() { stop(); closer() })
		})
	uuid := localNodeID + "/geneva/" + refuseUUID
	grid := uuid + "/" + rpc.KeyTileID("all")

	events := make(chan *gridwellv1.Event, 64)
	go func() {
		es, err := h.localCl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer es.Close()
		for {
			ev, ok, err := es.Recv()
			if err != nil || !ok {
				return
			}
			events <- ev
		}
	}()
	if err := h.localCl.SetInterest(ctx, []string{grid}); err != nil {
		t.Fatal(err)
	}
	health := func() *gridwellv1.EventPluginHealth {
		t.Helper()
		for {
			select {
			case ev := <-events:
				if hv := ev.GetPluginHealth(); hv != nil && hv.PluginUuid == uuid {
					return hv
				}
			case <-ctx.Done():
				t.Fatal("no health event for the refusing plugin reached the door")
			}
		}
	}
	off := health()
	if !off.Healthy || !strings.Contains(off.LiveUpdatesOff, "refused another change watch") {
		t.Fatalf("health = %v, want healthy with live updates off carrying the refusal", off)
	}
	g, err := h.localCl.GetGrid(ctx, grid)
	if err != nil {
		t.Fatalf("the listing of a source whose watch is refused: %v", err)
	}
	if len(g.Tiles) != 1 {
		t.Fatalf("listing = %v, want the one entry", g.Tiles)
	}
	// The refusal repeats on every re-open and is the same news each time.
	select {
	case ev := <-events:
		if hv := ev.GetPluginHealth(); hv != nil && hv.PluginUuid == uuid {
			t.Fatalf("then %v, want the refusal told once", hv)
		}
	case <-time.After(3 * time.Second):
	}
}
