package server_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
	"github.com/josephburnett/gridwell/internal/trace"
)

// apiEnded reports that the API has recorded a failed GetGrid of grid.
func apiEnded(grid string) bool {
	for _, r := range trace.Default().Snapshot() {
		if r.Kind == "rpc" && r.KV["id"] == grid && strings.HasPrefix(r.Msg, "GetGrid error") {
			return true
		}
	}
	return false
}

const abandonUUID = "pabandon"

// stallFirstList holds the first List after it is armed until the caller gives
// up, and answers every other one at once.
type stallFirstList struct {
	pluginv1.UnimplementedPluginServer
	armed   atomic.Bool
	entered chan struct{}
}

func (*stallFirstList) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "feed", DisplayName: "feed",
		MenuEntries: []*pluginv1.MenuEntry{{Id: "all", Label: "All", Context: "all"}}}, nil
}

func (p *stallFirstList) List(ctx context.Context, _ *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	if p.armed.CompareAndSwap(true, false) {
		close(p.entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &pluginv1.ListResponse{Authoritative: true}, nil
}

// A read the client abandons says nothing about the source: no health
// transition reaches a subscriber, because each one costs every client a
// resync of the source's grids, and that resync's own cancels were the storm
// of 2026-10-02.
func TestAnAbandonedReadIsNoHealthNews(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/gridwell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := &stallFirstList{entered: make(chan struct{})}
	cp, closer, err := plugintest.Loopback(p)
	if err != nil {
		t.Fatal(err)
	}
	a, stop := pluginhost.Start(cp, st.Namespace(abandonUUID), nil, "plugin "+abandonUUID)
	reg := plugin.NewRegistry()
	reg.Register(localNodeID, "home", local.New(st, nil), nil)
	reg.Register(abandonUUID, "feed", a, func() { stop(); closer() })
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	grid := rpc.QualifyID(abandonUUID, rpc.KeyTileID("all"))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	events := make(chan *gridwellv1.Event, 64)
	es, err := cl.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer es.Close()
		for {
			ev, ok, err := es.Recv()
			if err != nil || !ok {
				close(events)
				return
			}
			events <- ev
		}
	}()
	// A framing write is announced on the same stream, so its arrival marks
	// everything published before it as delivered.
	zoom := 1.0
	marker := func() {
		t.Helper()
		zoom++
		if _, err := cl.SetFraming(ctx, &gridwellv1.SetFramingRequest{RootGridId: grid, Zoom: zoom}); err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					t.Fatal("event stream ended")
				}
				if h := ev.GetPluginHealth(); h != nil {
					t.Fatalf("health event %v: an abandoned read was taken as news of the source", h)
				}
				if f := ev.GetGridFramingChanged(); f != nil && f.ViewZoom == zoom {
					return
				}
			case <-ctx.Done():
				t.Fatalf("framing marker %v never arrived", zoom)
			}
		}
	}
	marker()

	p.armed.Store(true)
	readCtx, abandon := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := cl.GetGrid(readCtx, grid); done <- err }()
	select {
	case <-p.entered:
	case <-ctx.Done():
		t.Fatal("the read never reached the plugin")
	}
	abandon()
	if err := <-done; err == nil {
		t.Fatal("the abandoned read answered")
	}
	// The API finishes the abandoned call after the client has given up on
	// it; its exit record is the moment the source's verdict, if any, is in.
	for !apiEnded(grid) {
		if ctx.Err() != nil {
			t.Fatal("the API never finished the abandoned read")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := cl.GetGrid(ctx, grid); err != nil {
		t.Fatal(err)
	}
	marker()
}
