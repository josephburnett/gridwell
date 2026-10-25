package server

// The content API carries a whole HTTP request to a plugin and its whole
// answer back: a browser's request crosses the web API, the router and the
// pluginhost adapter to a plugin.v1 server over gRPC, and the response
// streams back chunk by chunk. The plugin is in-process
// (plugintest.Loopback), because no shipped binary answers these fields
// until the api module is tagged with them.

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// echoPlugin serves one page entry: "echo" answers the request it was
// sent, "stream" answers three chunks, each only after release lets it.
type echoPlugin struct {
	pluginv1.UnimplementedPluginServer
	mu      sync.Mutex
	got     *pluginv1.ServeContentRequest
	release chan struct{}
}

func (p *echoPlugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "echo", Grids: []*pluginv1.PluginGrid{{Id: "g", Grid: "g"}}}, nil
}

func (p *echoPlugin) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	return &pluginv1.ListResponse{Authoritative: true, Entries: []*pluginv1.Entry{
		{Key: "p", Kind: rpc.KindURL, Label: "page", ServesPage: true},
	}}, nil
}

func (p *echoPlugin) ServeContent(req *pluginv1.ServeContentRequest, s pluginv1.Plugin_ServeContentServer) error {
	p.mu.Lock()
	p.got = req
	p.mu.Unlock()
	if req.GetSubpath() == "stream" {
		for i, part := range []string{"one\n", "two\n", "three\n"} {
			c := &pluginv1.ServeContentChunk{Data: []byte(part)}
			if i == 0 {
				c.Status, c.MediaType = 200, "text/plain"
			}
			if err := s.Send(c); err != nil {
				return err
			}
			select {
			case <-p.release:
			case <-s.Context().Done():
				return s.Context().Err()
			}
		}
		return nil
	}
	return s.Send(&pluginv1.ServeContentChunk{
		Status: 201, MediaType: "text/plain", Data: req.GetBody(),
		Headers: []*pluginv1.Header{
			{Name: "X-Echo-Method", Value: req.GetMethod()},
			{Name: "X-Echo-Query", Value: req.GetQuery()},
			{Name: "Cache-Control", Value: "no-store"},
			{Name: "Content-Security-Policy", Value: "default-src *"},
			{Name: "x-content-type-options", Value: "off"},
			{Name: "Content-Type", Value: "text/html"},
			{Name: "Set-Cookie", Value: AuthCookieName + "=plugin"},
		},
	})
}

func (p *echoPlugin) request() *pluginv1.ServeContentRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.got
}

// echoNamespace is echoPlugin behind the pluginhost adapter.
func echoNamespace(t *testing.T) (namespace.Namespace, *echoPlugin) {
	t.Helper()
	impl := &echoPlugin{release: make(chan struct{})}
	cp, closer, err := plugintest.Loopback(impl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	st, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return pluginhost.New(cp, st.Namespace("ue1"), nil), impl
}

// echoServer stands echoPlugin up behind the real web API and answers the
// page's content URL, without its trailing subpath.
func echoServer(t *testing.T) (*httptest.Server, *echoPlugin, string) {
	t.Helper()
	ns, impl := echoNamespace(t)
	reg := plugin.NewRegistry()
	reg.Register("ue1", "echo", ns, nil)
	hs := webAPITest(t, mustNew(t, reg, Config{Password: "pw"}).WebHandler())
	return hs, impl, hs.URL + "/content/" + ContentToken("pw") + "/ue1/" + rpc.EntryTileID("g", "p") + "/"
}

// A posted form reaches the plugin whole, its query and the page's headers
// beside it, and the plugin's answer comes back with its status, its body
// and its own headers. The node's credentials never reach the plugin.
func TestContentAPICarriesAPostAndItsAnswer(t *testing.T) {
	hs, impl, page := echoServer(t)
	req, _ := http.NewRequest(http.MethodPost, page+"form?a=1&b=two", strings.NewReader("name=gridwell"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Page", "yes")
	req.Header.Set("Authorization", "Bearer pw")
	req.AddCookie(&http.Cookie{Name: AuthCookieName, Value: AuthToken("pw")})
	res, err := hs.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode != 201 || string(body) != "name=gridwell" {
		t.Fatalf("POST = %d %q, want the plugin's 201 echoing the body", res.StatusCode, body)
	}
	if m, q := res.Header.Get("X-Echo-Method"), res.Header.Get("X-Echo-Query"); m != "POST" || q != "a=1&b=two" {
		t.Errorf("the plugin saw method %q query %q, want POST and the raw query", m, q)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("the plugin's own header = %q, want it written", got)
	}
	got := impl.request()
	if got.GetSubpath() != "form" {
		t.Errorf("subpath = %q, want form", got.GetSubpath())
	}
	sent := map[string]string{}
	for _, h := range got.GetHeaders() {
		sent[http.CanonicalHeaderKey(h.GetName())] = h.GetValue()
	}
	if sent["X-Page"] != "yes" || sent["Content-Type"] != "application/x-www-form-urlencoded" {
		t.Errorf("request headers = %v, want the page's", sent)
	}
	if _, ok := sent["Cookie"]; ok {
		t.Errorf("the auth cookie reached the plugin: %v", sent)
	}
	if _, ok := sent["Authorization"]; ok {
		t.Errorf("the bearer reached the plugin: %v", sent)
	}
}

// A header the node owns is the node's whatever the plugin answers: the
// sandbox, nosniff, the media type the chunk declares, and the origin's
// cookies, which are the node's credential.
func TestContentAPIKeepsTheNodesHeaders(t *testing.T) {
	hs, _, page := echoServer(t)
	res, err := hs.Client().Get(page)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	for name, want := range map[string]string{
		"Content-Security-Policy": contentSandbox,
		"X-Content-Type-Options":  "nosniff",
		"Content-Type":            "text/plain",
	} {
		if got := res.Header.Values(name); len(got) != 1 || got[0] != want {
			t.Errorf("%s = %q, want the node's %q alone", name, got, want)
		}
	}
	if got := res.Header.Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("the plugin set a cookie on the node's origin: %q", got)
	}
}

// Each chunk reaches the page before the plugin sends the next, so a
// streamed answer renders as it comes rather than when it ends.
func TestContentAPIFlushesEveryChunk(t *testing.T) {
	hs, impl, page := echoServer(t)
	t.Cleanup(func() { close(impl.release) }) // a failed run lets the plugin end
	lines := make(chan string)
	go func() {
		defer close(lines)
		res, err := hs.Client().Get(page + "stream")
		if err != nil {
			return
		}
		defer res.Body.Close()
		r := bufio.NewReader(res.Body)
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			lines <- l
		}
	}()
	for _, want := range []string{"one\n", "two\n", "three\n"} {
		select {
		case got := <-lines:
			if got != want {
				t.Fatalf("chunk = %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%q never reached the page before the plugin's next chunk", want)
		}
		impl.release <- struct{}{}
	}
}

// GET, HEAD and POST are the API's methods; any other is refused before a
// plugin is asked.
func TestContentAPIRefusesOtherMethods(t *testing.T) {
	hs, impl, page := echoServer(t)
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		req, _ := http.NewRequest(m, page, nil)
		res, err := hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", m, res.StatusCode)
		}
	}
	if impl.request() != nil {
		t.Error("a refused method reached the plugin")
	}
	res, err := hs.Client().Head(page)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 201 || impl.request().GetMethod() != http.MethodHead {
		t.Errorf("HEAD = %d, the plugin saw %q; want it carried", res.StatusCode, impl.request().GetMethod())
	}
}

// A body past the cap is refused before a plugin is asked, rather than
// failing as an oversized message at a hop.
func TestContentAPIRefusesAnOversizedBody(t *testing.T) {
	hs, impl, page := echoServer(t)
	res, err := hs.Client().Post(page, "application/octet-stream", bytes.NewReader(make([]byte, contentBodyCap+1)))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge || impl.request() != nil {
		t.Errorf("oversized POST = %d, asked %v; want 413 unasked", res.StatusCode, impl.request() != nil)
	}
}

// A far node's page takes the same request: the router and the connection
// carry it whole, as they carry every other field of the call.
func TestContentAPICarriesAPostThroughAConnection(t *testing.T) {
	far, impl := echoNamespace(t)
	connStore, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connStore.Close() })
	transport, err := connection.New(connStore, func(dial.Config) (namespace.Namespace, func(), error) {
		return far, func() {}, nil
	}, "", []config.ConnectionConfig{{Name: "far", Addr: "/tmp/far.sock"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	transport.ConnectAll(t.Context())
	reg := plugin.NewRegistry()
	reg.SetTransport(transport, nil)
	hs := serveWeb(t, mustNew(t, reg, Config{ID: "lnode1"}))

	page := hs.URL + "/content/" + ContentToken(testPassword) + "/lnode1/far/" + rpc.EntryTileID("g", "p") + "/form?q=1"
	res, err := hs.Client().Post(page, "text/plain", strings.NewReader("far body"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 201 || string(body) != "far body" || res.Header.Get("X-Echo-Query") != "q=1" {
		t.Fatalf("far POST = %d %q query %q, want the far plugin's echo", res.StatusCode, body, res.Header.Get("X-Echo-Query"))
	}
	if got := impl.request(); got.GetMethod() != http.MethodPost || got.GetSubpath() != "form" {
		t.Errorf("the far plugin saw %q %q, want POST form", got.GetMethod(), got.GetSubpath())
	}
}
