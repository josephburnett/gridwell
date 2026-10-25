package server_test

// A url tile whose address a route sends to a plugin that presents it is
// stamped presented_by on every answer, by the node holding the row: home's
// here, and a far node's through the connection. The presenter is in-process
// (plugintest.Loopback), because no shipped binary declares presents until
// the api module is tagged with it.

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"

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
)

const presenterID = "pre0001"

// presenter declares no grids and presents example.test; a page it serves
// says the subpath it was asked for, and its 404 is its own.
type presenter struct {
	pluginv1.UnimplementedPluginServer
	mu  sync.Mutex
	got *pluginv1.ServeContentRequest
}

func (p *presenter) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "pages", Presents: []string{"http://example.test/*"}}, nil
}

func (p *presenter) ServeContent(req *pluginv1.ServeContentRequest, s pluginv1.Plugin_ServeContentServer) error {
	p.mu.Lock()
	p.got = req
	p.mu.Unlock()
	if req.GetSubpath() == "example.test/missing" {
		return s.Send(&pluginv1.ServeContentChunk{Status: 404, MediaType: "text/plain", Data: []byte("no such page")})
	}
	return s.Send(&pluginv1.ServeContentChunk{Status: 200, MediaType: "text/html", Data: []byte("presented " + req.GetSubpath())})
}

// presentedRoutes send example.test to the presenter, and other.test to it
// too, which it does not present.
var presentedRoutes = []*gridwellv1.Route{
	{Match: "http://example.test/*", Plugin: presenterID},
	{Match: "http://other.test/*", Plugin: presenterID},
}

// registerPresenter registers the presenter beside a node's home.
func registerPresenter(t *testing.T, impl *presenter) func(*plugin.Registry, *store.Store) {
	return func(reg *plugin.Registry, st *store.Store) {
		t.Helper()
		cp, closer, err := plugintest.Loopback(impl)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closer)
		reg.Register(presenterID, "pages", pluginhost.New(cp, st.Namespace(presenterID), nil), nil)
	}
}

// presentingNode is a node with a home, the presenter and the routes, served
// on its web API.
func presentingNode(t *testing.T, impl *presenter) (*rpc.Client, *httptest.Server, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	reg.Register(localNodeID, "home", local.New(st, nil), nil)
	registerPresenter(t, impl)(reg, st)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID, Routes: presentedRoutes}))
	root, err := st.RootGridID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON()), hs, rpc.QualifyID(localNodeID, root)
}

func createURL(t *testing.T, cl *rpc.Client, grid string, x int64, address string) *gridwellv1.Tile {
	t.Helper()
	tl, err := cl.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: grid,
		Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: x, Y: 0, W: 1, H: 1, UrlString: address}})
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

// A home url tile a route and a declaration both take comes back presented,
// from the create and from a read of its grid; one a route takes but the
// plugin does not present, and one no route takes, do not.
func TestAHomeURLTileIsPresentedWhereARouteAndADeclarationTakeIt(t *testing.T) {
	ctx := context.Background()
	cl, _, root := presentingNode(t, &presenter{})
	cases := map[string]string{
		"http://example.test/a": presenterID,
		"http://other.test/a":   "",
		"http://plain.test/a":   "",
	}
	x := int64(0)
	for address, want := range cases {
		if got := createURL(t, cl, root, x, address).PresentedBy; got != want {
			t.Errorf("created %s: presented_by %q, want %q", address, got, want)
		}
		x++
	}
	g, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range g.Tiles {
		if want, ok := cases[tl.UrlString]; ok && tl.PresentedBy != want {
			t.Errorf("read %s: presented_by %q, want %q", tl.UrlString, tl.PresentedBy, want)
		}
	}
	h, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	routes, presents := rpc.Routing(h)
	if v := rpc.Decide("http://example.test/a", routes, presents, rpc.MatchAddress); len(routes) != 2 || v.Plugin != presenterID {
		t.Errorf("the handshake's routes %v and presents %v decide %+v, want the presenter", routes, presents, v)
	}
}

// A far node's url tile arrives presented by the far node's verdict, its
// plugin named from here; the near node, which has no routes, adds nothing.
func TestAFarURLTileArrivesPresentedByTheFarNodesVerdict(t *testing.T) {
	ctx := context.Background()
	far := newFarNodeWith(t, server.Config{ID: "rnode1", Routes: presentedRoutes}, registerPresenter(t, &presenter{}))
	createURL(t, far.cl, far.root, 0, "http://example.test/a")

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	reg.Register(localNodeID, "home", local.New(st, nil), nil)
	connectTo(t, "geneva", far, nil)(reg, st)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	near := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	g, err := near.GetGrid(ctx, localNodeID+"/geneva/"+far.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 1 || g.Tiles[0].PresentedBy != localNodeID+"/geneva/"+presenterID {
		t.Fatalf("far tiles = %v, want one presented by %s/geneva/%s", g.Tiles, localNodeID, presenterID)
	}
}
