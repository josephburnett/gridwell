package server_test

// A plugin's Watch stream, through pluginhost.Start as the loader runs it,
// reaching a client subscribed at the web door: on the node that hosts the
// plugin, and on a node that reaches it through a connection.

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

const watchUUID = "pwatch1"

// pokedWatch declares one collection and sends a ContextChanged for it on
// every poke.
type pokedWatch struct {
	pluginv1.UnimplementedPluginServer
	poke chan struct{}
}

func (pokedWatch) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "feed", DisplayName: "feed",
		MenuEntries: []*pluginv1.MenuEntry{{Id: "all", Label: "All", Context: "all"}}}, nil
}

func (pokedWatch) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	return &pluginv1.ListResponse{Authoritative: true}, nil
}

func (p pokedWatch) Watch(_ *pluginv1.WatchRequest, s grpc.ServerStreamingServer[pluginv1.Change]) error {
	for {
		select {
		case <-p.poke:
			if err := s.Send(&pluginv1.Change{Payload: &pluginv1.Change_ContextChanged{
				ContextChanged: &pluginv1.ContextChanged{Context: "all"}}}); err != nil {
				return err
			}
		case <-s.Context().Done():
			return nil
		}
	}
}

// registerWatching registers the plugin the way the loader does, through
// pluginhost.Start, and returns its poke.
func registerWatching(t *testing.T, reg *plugin.Registry, st *store.Store) chan<- struct{} {
	t.Helper()
	p := pokedWatch{poke: make(chan struct{}, 1)}
	cp, closer, err := plugintest.Loopback(p)
	if err != nil {
		t.Fatal(err)
	}
	a, stop := pluginhost.Start(cp, st.Namespace(watchUUID), nil, "plugin "+watchUUID+" watch")
	reg.Register(watchUUID, "feed", a, func() { stop(); closer() })
	reg.SetLabel(watchUUID, "feed")
	return p.poke
}

// awaitGridChanged subscribes at cl's door and pokes until a GridChanged for
// want arrives. Poking again covers the subscription attaching after an
// earlier poke, which is nobody's.
func awaitGridChanged(t *testing.T, cl *rpc.Client, poke chan<- struct{}, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	got := make(chan *gridwellv1.Event, 64)
	go func() {
		es, err := cl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer es.Close()
		for {
			ev, ok, err := es.Recv()
			if err != nil || !ok {
				return
			}
			got <- ev
		}
	}()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	var seen []string
	for {
		select {
		case ev := <-got:
			if id := ev.GetGridChanged().GetGridId(); id == want {
				return
			} else if id != "" {
				seen = append(seen, id)
			}
		case <-tick.C:
			select {
			case poke <- struct{}{}:
			default:
			}
		case <-ctx.Done():
			t.Fatalf("no GridChanged(%s) reached the door; saw %v", want, seen)
		}
	}
}

// A plugin that says a context changed reaches a client on its own node as the
// GridChanged a write would have published, under the qualified grid id the
// client holds.
func TestPluginWatchReachesTheDoor(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/gridwell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	reg.Register(localNodeID, "home", local.New(st, nil), nil)
	reg.SetLabel(localNodeID, "home")
	poke := registerWatching(t, reg, st)
	srv := servertest.New(t, reg, server.Config{ID: localNodeID})
	hs := servertest.Serve(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	lp, err := cl.Handshake(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var grid string
	for _, p := range lp.Plugins {
		if p.Uuid == watchUUID && len(p.MenuEntries) == 1 {
			grid = p.MenuEntries[0].GridId
		}
	}
	if want := rpc.QualifyID(watchUUID, rpc.KeyTileID("all")); grid != want {
		t.Fatalf("the collection's doorway is %q, want %q", grid, want)
	}
	awaitGridChanged(t, cl, poke, grid)
}

// The same change crosses a connection: a client of the node that mounts the
// far one sees it under the far grid's id through the connection segment.
func TestPluginWatchCrossesAConnection(t *testing.T) {
	var poke chan<- struct{}
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil,
		func(reg *plugin.Registry, st *store.Store) { poke = registerWatching(t, reg, st) })
	want := localNodeID + "/geneva/" + rpc.QualifyID(watchUUID, rpc.KeyTileID("all"))
	awaitGridChanged(t, h.localCl, poke, want)
}
