package server_test

// A url tile whose address a route sends to a plugin that presents it is
// stamped presented_by on every answer, by the node holding the row: home's
// here, and a far node's through the connection. The presenter is in-process
// (plugintest.Loopback), so a test can script its answers, except where the
// process itself is the case: the shipped pages binary, stopped or killed.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	return presentingNodeWith(t, registerPresenter(t, impl))
}

// presentingNodeWith is presentingNode with the presenter register registers.
func presentingNodeWith(t *testing.T, register func(*plugin.Registry, *store.Store)) (*rpc.Client, *httptest.Server, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	reg.Register(localNodeID, "home", local.New(st, nil), nil)
	register(reg, st)
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

// unmounted is the sentence a presenter whose source is down refuses Info
// with.
const unmounted = "the site's source is not mounted"

// downPresenter is the presenter with an Info that refuses while down, and
// counts every Info and page it is asked for.
type downPresenter struct {
	*presenter
	down         atomic.Bool
	infos, pages atomic.Int32
}

func (p *downPresenter) Info(ctx context.Context, req *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	p.infos.Add(1)
	if p.down.Load() {
		return nil, status.Error(codes.FailedPrecondition, unmounted)
	}
	return p.presenter.Info(ctx, req)
}

func (p *downPresenter) ServeContent(req *pluginv1.ServeContentRequest, s pluginv1.Plugin_ServeContentServer) error {
	p.pages.Add(1)
	return p.presenter.ServeContent(req, s)
}

// registerDown registers impl as the loader does (pluginhost.Start), so a
// refused Info is asked again on the fan-in's backoff, with a switch that
// disables it.
func registerDown(t *testing.T, impl *downPresenter) func(*plugin.Registry, *store.Store) {
	return func(reg *plugin.Registry, st *store.Store) {
		t.Helper()
		cp, closer, err := plugintest.Loopback(impl)
		if err != nil {
			t.Fatal(err)
		}
		a, stop := pluginhost.Start(cp, st.Namespace(presenterID), nil, "plugin "+presenterID+" watch")
		t.Cleanup(func() { stop(); closer() })
		reg.Register(presenterID, "pages", a, nil)
		reg.Switch(presenterID, "site", func() {})
	}
}

// urlRow is the url tile at address on grid, as a read of the grid answers it.
func urlRow(t *testing.T, cl *rpc.Client, grid, address string) *gridwellv1.Tile {
	t.Helper()
	g, err := cl.GetGrid(context.Background(), grid)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range g.Tiles {
		if tl.UrlString == address {
			return tl
		}
	}
	t.Fatalf("grid %s holds no url tile at %s", grid, address)
	return nil
}

// A url tile a route sends to a plugin whose Info refuses reads broken with
// the plugin's sentence, presented_by still naming it; its page loads
// nothing, neither the plugin's nor the site, and the plugin is not asked
// for one. Once Info answers, with no restart, the tile is presented again
// and its page is the plugin's.
func TestAURLTileWhosePluginRefusesInfoIsBrokenUntilItAnswers(t *testing.T) {
	impl := &downPresenter{presenter: &presenter{}}
	impl.down.Store(true)
	cl, hs, root := presentingNodeWith(t, registerDown(t, impl))
	created := createURL(t, cl, root, 0, "http://example.test/a")
	if created.PresentedBy != presenterID || created.BrokenReason != unmounted {
		t.Fatalf("created: presented_by %q broken %q, want %s broken with %q", created.PresentedBy, created.BrokenReason, presenterID, unmounted)
	}
	if got := urlRow(t, cl, root, "http://example.test/a"); got.PresentedBy != presenterID || got.BrokenReason != unmounted {
		t.Errorf("read: presented_by %q broken %q, want broken with the reason", got.PresentedBy, got.BrokenReason)
	}
	if plain := createURL(t, cl, root, 1, "http://plain.test/a"); plain.PresentedBy != "" || plain.BrokenReason != "" {
		t.Errorf("a tile no route takes: presented_by %q broken %q, want neither", plain.PresentedBy, plain.BrokenReason)
	}
	page := strings.TrimSuffix(rpc.PageURL("", server.ContentToken(servertest.Password), created.Id), "/")
	if code, body := get(t, hs, page+"/example.test/a"); code == 200 || !strings.Contains(body, unmounted) {
		t.Errorf("the broken page = %d %q, want a refusal with the reason", code, body)
	}
	if code, location := navigate(t, hs, page+"/example.test/b", document); code == http.StatusSeeOther || location != "" {
		t.Errorf("a document under the broken page = %d %q, want no redirect to the site", code, location)
	}
	if n := impl.pages.Load(); n != 0 {
		t.Errorf("the plugin was asked for %d pages while broken", n)
	}

	impl.down.Store(false)
	deadline := time.Now().Add(15 * time.Second)
	for got := urlRow(t, cl, root, "http://example.test/a"); got.BrokenReason != "" || got.PresentedBy != presenterID; got = urlRow(t, cl, root, "http://example.test/a") {
		if time.Now().After(deadline) {
			t.Fatalf("after Info answers: presented_by %q broken %q, want presented", got.PresentedBy, got.BrokenReason)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, body := get(t, hs, page+"/example.test/a"); code != 200 || body != "presented example.test/a" {
		t.Errorf("the page once Info answers = %d %q, want the plugin's", code, body)
	}
}

// A disabled plugin is asked nothing: a url tile a route sends to it reads
// broken with the registry's sentence, and the handshake's row carries it,
// so the client's verdict is the node's.
func TestAURLTileWhosePluginIsDisabledIsBrokenAndThePluginIsNotAsked(t *testing.T) {
	impl := &downPresenter{presenter: &presenter{}}
	var reg *plugin.Registry
	cl, hs, root := presentingNodeWith(t, func(r *plugin.Registry, st *store.Store) {
		reg = r
		registerDown(t, impl)(r, st)
	})
	if err := reg.Disable(presenterID); err != nil {
		t.Fatal(err)
	}
	reason := reg.DisabledReason(presenterID)
	asked := impl.infos.Load()
	created := createURL(t, cl, root, 0, "http://example.test/a")
	if created.PresentedBy != presenterID || created.BrokenReason != reason {
		t.Errorf("created: presented_by %q broken %q, want %s broken with %q", created.PresentedBy, created.BrokenReason, presenterID, reason)
	}
	page := strings.TrimSuffix(rpc.PageURL("", server.ContentToken(servertest.Password), created.Id), "/")
	if code, body := get(t, hs, page+"/example.test/a"); code == 200 || !strings.Contains(body, reason) {
		t.Errorf("the disabled page = %d %q, want a refusal with the reason", code, body)
	}
	if n := impl.infos.Load() - asked; n != 0 {
		t.Errorf("the disabled plugin's Info was asked %d times by the stamp", n)
	}
	h, err := cl.Handshake(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	routes, declared := rpc.Routing(h)
	if v := rpc.Decide("http://example.test/a", routes, declared, rpc.MatchAddress); v.Plugin != presenterID || v.Broken != reason {
		t.Errorf("the handshake decides %+v, want the node's broken verdict", v)
	}
}

// A far url tile whose far plugin refuses Info arrives broken by the far
// node's verdict, the reason carried through the connection.
func TestAFarURLTileArrivesBrokenByTheFarNodesVerdict(t *testing.T) {
	ctx := context.Background()
	impl := &downPresenter{presenter: &presenter{}}
	impl.down.Store(true)
	far := newFarNodeWith(t, server.Config{ID: "rnode1", Routes: presentedRoutes}, registerDown(t, impl))
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
	if tl := g.Tiles[0]; tl.PresentedBy != localNodeID+"/geneva/"+presenterID || tl.BrokenReason != unmounted {
		t.Errorf("far tile: presented_by %q broken %q, want the far verdict", tl.PresentedBy, tl.BrokenReason)
	}
}

// registerSupervised registers the shipped pages binary, presenting
// example.test, under its supervisor as the loader does (plugin.LoadInto).
func registerSupervised(t *testing.T) func(*plugin.Registry, *store.Store) {
	return func(reg *plugin.Registry, st *store.Store) {
		t.Helper()
		sup, err := plugin.Supervise(presenterID, "pages", plugintest.Binary(t, "pages"), map[string]string{
			"uuid": presenterID, "kind": "pages", "state_dir": t.TempDir(), "presents": "http://example.test/*",
		})
		if err != nil {
			t.Fatal(err)
		}
		a, stop := pluginhost.Start(pluginv1.NewPluginClient(sup), st.Namespace(presenterID), sup, "plugin "+presenterID+" watch")
		closer := func() { stop(); sup.Close() }
		t.Cleanup(closer)
		reg.Register(presenterID, "pages", a, closer)
		reg.Switch(presenterID, "site", sup.Disable)
	}
}

// pagesPID is the pid of the one pages subprocess this test spawned. The
// supervisor exposes no handle to its process, so a signal goes by pid.
func pagesPID(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid()), "-f", "gridwell-plugin-pages").Output()
	if err != nil {
		t.Fatalf("pgrep for the pages subprocess: %v", err)
	}
	pids := strings.Fields(string(out))
	if len(pids) != 1 {
		t.Fatalf("pgrep found %d pages children, want the one this test spawned: %q", len(pids), out)
	}
	pid, err := strconv.Atoi(pids[0])
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// readFast is the url tile at address on grid, read in well under
// pluginInfoTimeout: a verdict reads the plugin's state and asks nothing.
func readFast(t *testing.T, cl *rpc.Client, grid, address string) *gridwellv1.Tile {
	t.Helper()
	start := time.Now()
	tl := urlRow(t, cl, grid, address)
	if took := time.Since(start); took > server.PluginInfoTimeout/6 {
		t.Errorf("a read took %v, want it well under pluginInfoTimeout (%v)", took, server.PluginInfoTimeout)
	}
	return tl
}

// awaitTile reads the tile until want holds, within pluginInfoTimeout and a
// second: the probe's bound.
func awaitTile(t *testing.T, cl *rpc.Client, grid, address, what string, want func(*gridwellv1.Tile) bool) {
	t.Helper()
	deadline := time.Now().Add(server.PluginInfoTimeout + time.Second)
	for got := readFast(t, cl, grid, address); !want(got); got = readFast(t, cl, grid, address) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: presented_by %q broken %q", what, got.PresentedBy, got.BrokenReason)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A url tile a route sends to a plugin whose process is stopped reads broken
// once a call goes unanswered: the page, opened and given up on, kicks one
// probe, and its Info unanswered by pluginInfoTimeout is the verdict. No read
// waits on the process. Once it runs again, the next read probes it and the
// tile is presented again, with no restart.
func TestAURLTileWhosePluginsProcessIsStoppedIsBrokenUntilItAnswers(t *testing.T) {
	cl, hs, root := presentingNodeWith(t, registerSupervised(t))
	created := createURL(t, cl, root, 0, "http://example.test/a")
	if created.PresentedBy != presenterID || created.BrokenReason != "" {
		t.Fatalf("created: presented_by %q broken %q, want presented", created.PresentedBy, created.BrokenReason)
	}
	health := presenterHealth(t, cl)
	pid := pagesPID(t)
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGCONT) })
	if got := readFast(t, cl, root, "http://example.test/a"); got.BrokenReason != "" {
		t.Errorf("stopped, nothing asked yet: broken %q, want presented", got.BrokenReason)
	}

	page := strings.TrimSuffix(rpc.PageURL("", server.ContentToken(servertest.Password), created.Id), "/")
	opener := *hs.Client()
	opener.Timeout = 300 * time.Millisecond
	if res, err := opener.Get(hs.URL + page + "/example.test/a"); err == nil {
		res.Body.Close()
		t.Fatalf("the stopped plugin's page answered %d", res.StatusCode)
	}
	awaitTile(t, cl, root, "http://example.test/a", "stopped", func(tl *gridwellv1.Tile) bool {
		return tl.PresentedBy == presenterID && strings.Contains(tl.BrokenReason, "not responding")
	})
	if h := awaitHealth(t, health, false); !strings.Contains(h.Detail, "not responding") {
		t.Errorf("stopped: health %+v, want the plugin told not responding", h)
	}

	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	awaitTile(t, cl, root, "http://example.test/a", "running again", func(tl *gridwellv1.Tile) bool {
		return tl.PresentedBy == presenterID && tl.BrokenReason == ""
	})
	awaitHealth(t, health, true)
}

// awaitHealth is the presenter's next health event saying healthy, within
// the probe's bound.
func awaitHealth(t *testing.T, health <-chan *gridwellv1.EventPluginHealth, healthy bool) *gridwellv1.EventPluginHealth {
	t.Helper()
	deadline := time.After(server.PluginInfoTimeout + time.Second)
	for {
		select {
		case h := <-health:
			if h.Healthy == healthy {
				return h
			}
		case <-deadline:
			t.Fatalf("no health event saying healthy=%v", healthy)
		}
	}
}

// presenterHealth is the presenter's health events on the client's stream,
// opened before it returns.
func presenterHealth(t *testing.T, cl *rpc.Client) <-chan *gridwellv1.EventPluginHealth {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream, err := cl.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan *gridwellv1.EventPluginHealth, 16)
	go func() {
		defer stream.Close()
		for {
			ev, ok, err := stream.Recv()
			if !ok || err != nil {
				return
			}
			if h := ev.GetPluginHealth(); h != nil && h.PluginUuid == presenterID {
				out <- h
			}
		}
	}()
	return out
}

// countingPresenter is the presenter counting every Info it is asked.
type countingPresenter struct {
	*presenter
	infos atomic.Int32
}

func (p *countingPresenter) Info(ctx context.Context, req *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	p.infos.Add(1)
	return p.presenter.Info(ctx, req)
}

// A plugin that answers is asked nothing by the verdict: grid answers holding
// a tile it presents read its state.
func TestAnAnsweringPluginIsAskedNoInfoByTheVerdict(t *testing.T) {
	impl := &countingPresenter{presenter: &presenter{}}
	cl, _, root := presentingNodeWith(t, func(reg *plugin.Registry, st *store.Store) {
		cp, closer, err := plugintest.Loopback(impl)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closer)
		reg.Register(presenterID, "pages", pluginhost.New(cp, st.Namespace(presenterID), nil), nil)
	})
	createURL(t, cl, root, 0, "http://example.test/a")
	asked := impl.infos.Load()
	for range 5 {
		if got := urlRow(t, cl, root, "http://example.test/a"); got.PresentedBy != presenterID || got.BrokenReason != "" {
			t.Fatalf("presented_by %q broken %q, want presented", got.PresentedBy, got.BrokenReason)
		}
	}
	if _, err := cl.Handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := impl.infos.Load() - asked; n != 0 {
		t.Errorf("the verdict asked the answering plugin's Info %d times over five reads and a handshake", n)
	}
}

// A url tile a route sends to a plugin whose process died reads broken with
// the supervisor's reason until the respawn answers, and presented after.
func TestAURLTileWhosePluginsProcessDiedIsBrokenUntilTheRespawn(t *testing.T) {
	cl, _, root := presentingNodeWith(t, registerSupervised(t))
	createURL(t, cl, root, 0, "http://example.test/a")
	pid := pagesPID(t)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	broken := ""
	deadline := time.Now().Add(15 * time.Second)
	for {
		got := urlRow(t, cl, root, "http://example.test/a")
		if broken == "" && got.BrokenReason != "" {
			if got.PresentedBy != presenterID {
				t.Errorf("down: presented_by %q, want %s", got.PresentedBy, presenterID)
			}
			broken = got.BrokenReason
		}
		if broken != "" && got.BrokenReason == "" {
			if got.PresentedBy != presenterID {
				t.Errorf("respawned: presented_by %q, want %s", got.PresentedBy, presenterID)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the kill: broken seen %q, last presented_by %q broken %q; want broken, then presented", broken, got.PresentedBy, got.BrokenReason)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(broken, "not responding") {
		t.Errorf("down: broken %q, want the plugin not responding", broken)
	}
	if newPID := pagesPID(t); newPID == pid {
		t.Errorf("pid after the respawn = %d, want a new process", newPID)
	}
}
