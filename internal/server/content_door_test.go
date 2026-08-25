package server

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/proxytest"
	fsprovider "github.com/josephburnett/gridwell/plugins/fs/provider"
)

// The /content/ door crosses every seam at once: HTTP URL grammar → token
// gate → router (with link resolution and transit forwarding) → plugin
// ServeContent stream → HTTP response with the sandbox header. These tests
// drive the REAL NodeHandler with real plugins behind it — the exact
// surface a sandboxed page's subresource request hits.

// TestParseContentPath pins the URL grammar: the first all-digits segment
// terminates the qualified tile id (the id-shape owner decision — namespace
// segments are never purely numeric), and ".." never survives.
func TestParseContentPath(t *testing.T) {
	cases := []struct {
		path                   string
		token, tileID, subpath string
		needSlash, ok          bool
	}{
		{"/content/tok/uf1/5/", "tok", "uf1/5", "", false, true},
		{"/content/tok/uf1/5", "tok", "uf1/5", "", true, true},
		{"/content/tok/uf1/5/img/cat.png", "tok", "uf1/5", "img/cat.png", false, true},
		{"/content/tok/ssh1/conn/uf1/5/a.css", "tok", "ssh1/conn/uf1/5", "a.css", false, true},
		{"/content/tok/uf1/5/../secret", "", "", "", false, false},
		{"/content/tok/uf1/", "", "", "", false, false},     // no numeric local id
		{"/content/tok/uf1/abc/", "", "", "", false, false}, // ditto
		{"/content/tok", "", "", "", false, false},          // token alone
		{"/elsewhere/tok/uf1/5/", "", "", "", false, false}, // wrong prefix
	}
	for _, c := range cases {
		token, tileID, subpath, needSlash, ok := parseContentPath(c.path)
		if token != c.token || tileID != c.tileID || subpath != c.subpath || needSlash != c.needSlash || ok != c.ok {
			t.Errorf("parse(%q) = (%q,%q,%q,%v,%v), want (%q,%q,%q,%v,%v)",
				c.path, token, tileID, subpath, needSlash, ok,
				c.token, c.tileID, c.subpath, c.needSlash, c.ok)
		}
	}
}

// TestPageURLMatchesDoorGrammar pins the two sides of the URL contract to
// each other: rpc.PageURL (what the client builds) must parse back through
// parseContentPath (what the door accepts) with nothing lost — the same
// derive-once discipline as every other seam, so the grammar can never
// drift apart silently.
func TestPageURLMatchesDoorGrammar(t *testing.T) {
	for _, id := range []string{"uf1/5", "ssh1/conn/uf1/5", "1b467bbd65466256f8a64c538cabdac8/12"} {
		u := rpc.PageURL("https://host:1234", ContentToken("pw"), id)
		path := strings.TrimPrefix(u, "https://host:1234")
		token, tileID, subpath, needSlash, ok := parseContentPath(path)
		if !ok || needSlash || token != ContentToken("pw") || tileID != id || subpath != "" {
			t.Errorf("PageURL(%q) = %q does not round-trip: (%q,%q,%q,%v,%v)",
				id, u, token, tileID, subpath, needSlash, ok)
		}
	}
}

// contentDoorServer stands up a NodeHandler with an fs plugin over a temp
// dir holding one image, and returns the base URL plus the image tile's
// qualified id and the raw bytes.
func contentDoorServer(t *testing.T, password string) (hs *httptest.Server, tileID string, img []byte) {
	t.Helper()
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	img = buf.Bytes()
	if err := os.WriteFile(filepath.Join(dir, "cat.png"), img, 0o644); err != nil {
		t.Fatal(err)
	}

	fsClient := newProviderClient(t, "fs", fsprovider.New(dir, nil))

	reg := plugin.NewRegistry()
	reg.Register("uf1", "fs", fsClient, nil)
	srv := New(reg, Config{NodeID: "node1", Password: password})
	hs = httptest.NewServer(srv.NodeHandler())
	t.Cleanup(hs.Close)

	ctx := context.Background()
	info, err := fsClient.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	grid, err := fsClient.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range grid.Tiles {
		if tl.AltText == "cat.png" {
			return hs, "uf1/" + tl.Id, img
		}
	}
	t.Fatal("no cat.png tile")
	return nil, "", nil
}

// TestContentDoorServesImage: the happy path end to end, WITHOUT any auth
// cookie — the door's own token is the whole credential, because sandboxed
// subresource requests and the desktop's native views can never present the
// cookie.
func TestContentDoorServesImage(t *testing.T) {
	hs, tileID, img := contentDoorServer(t, "pw")
	token := ContentToken("pw")

	res, body := get(t, noRedirect(hs), hs.URL+"/content/"+token+"/"+tileID+"/", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET page = %d %q, want 200", res.StatusCode, body)
	}
	if body != string(img) {
		t.Errorf("body = %d bytes, want the file's %d", len(body), len(img))
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	// The sandbox header is the door's INVARIANT: opaque origin, scripts
	// allowed, no credentials, no reach back into Gridwell.
	if csp := res.Header.Get("Content-Security-Policy"); csp != "sandbox allow-scripts" {
		t.Errorf("CSP = %q, want \"sandbox allow-scripts\"", csp)
	}
	if xc := res.Header.Get("X-Content-Type-Options"); xc != "nosniff" {
		t.Errorf("nosniff header = %q", xc)
	}

	// The root page redirects to its trailing-slash home (relative
	// subresource URLs resolve against the directory).
	res, _ = get(t, noRedirect(hs), hs.URL+"/content/"+token+"/"+tileID, "")
	if res.StatusCode != http.StatusMovedPermanently || !strings.HasSuffix(res.Header.Get("Location"), tileID+"/") {
		t.Errorf("no-slash GET = %d loc %q, want 301 to .../", res.StatusCode, res.Header.Get("Location"))
	}
}

// TestContentDoorTokenGate: the wrong token — including the AUTH token — is
// a 401; the content token replayed as an auth cookie opens nothing. The
// two derivations live in different domains on purpose.
func TestContentDoorTokenGate(t *testing.T) {
	hs, tileID, _ := contentDoorServer(t, "pw")

	if res, _ := get(t, noRedirect(hs), hs.URL+"/content/"+ContentToken("wrong")+"/"+tileID+"/", ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token = %d, want 401", res.StatusCode)
	}
	if res, _ := get(t, noRedirect(hs), hs.URL+"/content/"+AuthToken("pw")+"/"+tileID+"/", ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("auth token as content token = %d, want 401 (domain separation)", res.StatusCode)
	}
	if res, _ := get(t, noRedirect(hs), hs.URL+"/", ContentToken("pw")); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("content token as auth cookie = %d, want 401 (domain separation)", res.StatusCode)
	}

	// GET-only surface.
	res, err := hs.Client().Post(hs.URL+"/content/"+ContentToken("pw")+"/"+tileID+"/", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", res.StatusCode)
	}
}

// TestContentDoorNoPassword: with no password the door still answers — under
// the empty-password token, the same uniform grammar (the client always
// learns the token from the handshake, so there is no special case).
func TestContentDoorNoPassword(t *testing.T) {
	hs, tileID, img := contentDoorServer(t, "")
	res, body := get(t, noRedirect(hs), hs.URL+"/content/"+ContentToken("")+"/"+tileID+"/", "")
	if res.StatusCode != http.StatusOK || body != string(img) {
		t.Fatalf("no-password GET = %d, %d bytes; want 200 with the image", res.StatusCode, len(body))
	}
}

// TestContentDoorResolvesLeafLink: a localdb LINK to an fs image serves the
// TARGET's bytes — the door inherits contentRoute, the one resolution
// point, like every other content read.
func TestContentDoorResolvesLeafLink(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	img := buf.Bytes()
	if err := os.WriteFile(filepath.Join(dir, "cat.png"), img, 0o644); err != nil {
		t.Fatal(err)
	}
	fsClient := newProviderClient(t, "fs", fsprovider.New(dir, nil))

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	_, rootA := registerPrimaryLocaldb(t, reg, st)
	reg.Register("uf1", "fs", fsClient, nil)
	srv := New(reg, Config{NodeID: "node1"})
	hs := httptest.NewServer(srv.NodeHandler())
	t.Cleanup(hs.Close)

	ctx := context.Background()
	info, err := fsClient.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	grid, err := fsClient.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	var catID string
	for _, tl := range grid.Tiles {
		if tl.AltText == "cat.png" {
			catID = "uf1/" + tl.Id
		}
	}
	cl := rpc.NewClient(hs.Client(), hs.URL)
	link, err := cl.CreateLeafLink(ctx, &rpc.CreateLeafLinkRequest{
		GridID: rootA, X: 0, Y: 0, W: 2, H: 2, Kind: rpc.KindText,
		LinkTargetID: catID, Label: "cat.png",
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	res, body := get(t, noRedirect(hs), hs.URL+"/content/"+ContentToken("")+"/"+link.ID+"/", "")
	if res.StatusCode != http.StatusOK || body != string(img) {
		t.Fatalf("link GET = %d, %d bytes; want the target's %d image bytes", res.StatusCode, len(body), len(img))
	}
}

// TestContentDoorTransit: the id "ssh1/<tile>" forwards through a transit
// plugin (the proxy — the ssh shape) and streams back — federation for
// pages is the same one-hop-per-segment composition as every content read.
func TestContentDoorTransit(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	img := buf.Bytes()
	if err := os.WriteFile(filepath.Join(dir, "remote.png"), img, 0o644); err != nil {
		t.Fatal(err)
	}
	fsClient := newProviderClient(t, "fs", fsprovider.New(dir, nil))
	proxied, proxClose, err := plugin.ServeInProcess(proxytest.New(fsClient))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxClose)

	reg := plugin.NewRegistry()
	reg.Register("ssh1", "remote", proxied, nil)
	reg.SetTransit("ssh1", true) // the declaration the loader reads from Info in production
	srv := New(reg, Config{NodeID: "node1"})
	hs := httptest.NewServer(srv.NodeHandler())
	t.Cleanup(hs.Close)

	ctx := context.Background()
	info, err := fsClient.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	grid, err := fsClient.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	var tid string
	for _, tl := range grid.Tiles {
		if tl.AltText == "remote.png" {
			tid = tl.Id
		}
	}
	res, body := get(t, noRedirect(hs), hs.URL+"/content/"+ContentToken("")+"/ssh1/"+tid+"/", "")
	if res.StatusCode != http.StatusOK || body != string(img) {
		t.Fatalf("transit GET = %d, %d bytes; want the remote image's %d", res.StatusCode, len(body), len(img))
	}
}

// TestContentDoorUnimplemented: a plugin that serves no web content answers
// 404, never a 500 — no pages is a normal property of a plugin.
func TestContentDoorUnimplemented(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	_, root := registerPrimaryLocaldb(t, reg, st)
	srv := New(reg, Config{NodeID: "node1"})
	hs := httptest.NewServer(srv.NodeHandler())
	t.Cleanup(hs.Close)

	cl := rpc.NewClient(hs.Client(), hs.URL)
	txt, err := cl.CreateText(context.Background(), &rpc.CreateTextRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("hi"),
	})
	if err != nil {
		t.Fatal(err)
	}
	res, _ := get(t, noRedirect(hs), hs.URL+"/content/"+ContentToken("")+"/"+txt.ID+"/", "")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("localdb page GET = %d, want 404 (ServeContent unimplemented)", res.StatusCode)
	}
}

// TestHandshakeCarriesTokensAndNodeView pins the ListPlugins seam facts
// that only ever had producer-side coverage: the content token (must be
// the CONTENT derivation, never the auth token) and the node grid's own
// root view — including through a raw BEACON post, the exact wire the
// unload flush uses (application/json unary, no Connect header), so the
// last-pan-survives-quit path rests on a tested seam.
func TestHandshakeCarriesTokensAndNodeView(t *testing.T) {
	hs, _, _ := contentDoorServer(t, "")
	cl := rpc.NewClient(hs.Client(), hs.URL)
	ctx := context.Background()

	pl, err := cl.ListPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pl.ContentToken != ContentToken("") {
		t.Errorf("handshake content token = %q, want ContentToken(password)", pl.ContentToken)
	}
	if pl.ContentToken == AuthToken("") {
		t.Errorf("content token must never equal the auth token (domain separation)")
	}
	if pl.NodeRootViewZoom != 0 {
		t.Errorf("fresh node root view zoom = %v, want 0 (never set)", pl.NodeRootViewZoom)
	}

	path, body := rpc.SetRootViewBeacon(&rpc.SetRootViewRequest{
		RootGridID: "node1/0", Cx: 3, Cy: 4, Zoom: 0.5,
	})
	res, err := hs.Client().Post(hs.URL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("beacon-shaped SetRootView POST = %d, want 200", res.StatusCode)
	}
	pl2, err := cl.ListPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pl2.NodeRootViewCx != 3 || pl2.NodeRootViewCy != 4 || pl2.NodeRootViewZoom != 0.5 {
		t.Errorf("node root view after beacon = (%v,%v,%v), want (3,4,0.5)",
			pl2.NodeRootViewCx, pl2.NodeRootViewCy, pl2.NodeRootViewZoom)
	}
}
