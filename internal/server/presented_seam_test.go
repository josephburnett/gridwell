package server_test

// A url tile whose address a route sends to a plugin that presents it is
// stamped presented_by on every answer, by the node holding the row: home's
// here, and a far node's through the connection. The presenter is in-process
// (plugintest.Loopback), because no shipped binary declares presents until
// the api module is tagged with it.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
// plugin named from here, and its page is the far plugin's; the near node,
// which has no routes, adds nothing.
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
	page := strings.TrimSuffix(rpc.PageURL("", server.ContentToken(servertest.Password), g.Tiles[0].Id), "/")
	if code, body := get(t, hs, page+"/example.test/a"); code != 200 || body != "presented example.test/a" {
		t.Errorf("the far page = %d %q, want the far plugin's", code, body)
	}
}

func (p *presenter) request() *pluginv1.ServeContentRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.got
}

// get answers status and body of the content API at path.
func get(t *testing.T, hs *httptest.Server, path string) (int, string) {
	t.Helper()
	res, err := hs.Client().Get(hs.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

// A presented tile's page is its plugin's: asked by no key, with the site
// address as the subpath, the plugin's 404 the page's answer. A link to it
// serves through the same hop, and a tile no route takes has no page.
func TestAPresentedTileIsServedByItsPlugin(t *testing.T) {
	impl := &presenter{}
	cl, hs, root := presentingNode(t, impl)
	presented := createURL(t, cl, root, 0, "http://example.test/a")
	plain := createURL(t, cl, root, 1, "http://plain.test/a")
	link, err := cl.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: root,
		Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 2, Y: 0, W: 1, H: 1, LinkTargetId: presented.Id}})
	if err != nil {
		t.Fatal(err)
	}
	page := func(id string) string {
		return strings.TrimSuffix(rpc.PageURL("", server.ContentToken(servertest.Password), id), "/")
	}
	if code, body := get(t, hs, page(presented.Id)+"/example.test/a?x=1"); code != 200 || body != "presented example.test/a" {
		t.Errorf("the presented page = %d %q, want the plugin's", code, body)
	}
	if got := impl.request(); got.GetKey() != "" || got.GetSubpath() != "example.test/a" || got.GetQuery() != "x=1" {
		t.Errorf("the plugin was asked key %q subpath %q query %q, want no key and the address", got.GetKey(), got.GetSubpath(), got.GetQuery())
	}
	if code, body := get(t, hs, page(presented.Id)+"/example.test/missing"); code != 404 || body != "no such page" {
		t.Errorf("a page the plugin has not = %d %q, want its own 404", code, body)
	}
	if code, body := get(t, hs, page(link.Id)+"/example.test/b"); code != 200 || body != "presented example.test/b" {
		t.Errorf("through the link = %d %q, want the plugin's", code, body)
	}
	if code, _ := get(t, hs, page(plain.Id)+"/plain.test/a"); code != 404 {
		t.Errorf("a tile no route takes = %d, want 404", code)
	}
}

// navigate asks the content API at path as a browser tab asks for a document
// (headers name the request's kind), and answers the status and where it
// redirects, without following.
func navigate(t *testing.T, hs *httptest.Server, path string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, hs.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	cl := *hs.Client()
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode, res.Header.Get("Location")
}

var (
	document = map[string]string{"Sec-Fetch-Dest": "document", "Sec-Fetch-Mode": "navigate"}
	script   = map[string]string{"Sec-Fetch-Dest": "script", "Sec-Fetch-Mode": "no-cors", "Accept": "*/*"}
	// A browser on a page that is no secure context sends no fetch metadata.
	insecure = map[string]string{"Accept": "text/html,application/xhtml+xml"}
)

// A browser tab follows the rule a desktop view keeps by asking first: a
// document under a presented tile's page for an address its plugin does not
// take is a redirect to the site, and the plugin is never asked; what its
// plugin takes, a sub-resource and the plugin's own assets stay its.
func TestADocumentItsPluginDoesNotTakeRedirectsToTheSite(t *testing.T) {
	impl := &presenter{}
	cl, hs, root := presentingNode(t, impl)
	presented := createURL(t, cl, root, 0, "http://example.test/a")
	page := strings.TrimSuffix(rpc.PageURL("", server.ContentToken(servertest.Password), presented.Id), "/")
	cases := []struct {
		name, path string
		headers    map[string]string
		code       int
		location   string
	}{
		{"a document the plugin takes", "/example.test/b", document, 200, ""},
		{"a document no route takes", "/plain.test/x?y=1", document, http.StatusSeeOther, "http://plain.test/x?y=1"},
		{"a document a route sends to a plugin that does not present it", "/other.test/a", document, http.StatusSeeOther, "http://other.test/a"},
		{"a document with no fetch metadata", "/plain.test/x", insecure, http.StatusSeeOther, "http://plain.test/x"},
		{"a sub-resource is the plugin's", "/plain.test/x", script, 200, ""},
		{"the plugin's assets are its", "/_/app.js", document, 200, ""},
	}
	for _, c := range cases {
		code, location := navigate(t, hs, page+c.path, c.headers)
		if code != c.code || location != c.location {
			t.Errorf("%s: %d %q, want %d %q", c.name, code, location, c.code, c.location)
		}
	}
	if got := impl.request().GetSubpath(); got != "_/app.js" {
		t.Errorf("the plugin was last asked for %q: a redirected document reached it", got)
	}
}

// A far tile's page is its own node's to judge, so the redirect comes back
// through the connection.
func TestAFarDocumentItsPluginDoesNotTakeRedirectsToTheSite(t *testing.T) {
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
	g, err := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON()).GetGrid(ctx, localNodeID+"/geneva/"+far.root)
	if err != nil || len(g.Tiles) != 1 {
		t.Fatalf("far grid = %v %v", g, err)
	}
	page := strings.TrimSuffix(rpc.PageURL("", server.ContentToken(servertest.Password), g.Tiles[0].Id), "/")
	if code, location := navigate(t, hs, page+"/plain.test/x", document); code != http.StatusSeeOther || location != "http://plain.test/x" {
		t.Errorf("a far document = %d %q, want a redirect to the site", code, location)
	}
	if code, _ := navigate(t, hs, page+"/example.test/b", document); code != 200 {
		t.Errorf("a far document its plugin takes = %d, want the plugin's", code)
	}
}
