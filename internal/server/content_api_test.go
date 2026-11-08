package server

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// The /content/ API crosses every seam at once: HTTP URL grammar → token
// gate → router (with link resolution and transit forwarding) → the files'
// ServeContent stream → HTTP response with the sandbox header. These tests
// drive the REAL WebHandler with the node's files behind it — the exact
// surface a sandboxed page's subresource request hits.

// TestParseContentPath pins the URL grammar: the qualified tile id ends at
// its first tile segment, since a namespace segment is never one, and
// nothing follows it: there is no subpath.
func TestParseContentPath(t *testing.T) {
	cases := []struct {
		path          string
		token, tileID string
		ok            bool
	}{
		{"/content/tok/uf1/5", "tok", "uf1/5", true},
		// A tile the node has never minted a row for is named by its key,
		// and the API reads that shape as the end of the id exactly as it
		// reads digits, or an untouched page would 404.
		{"/content/tok/uf1/~L2hvbWU", "tok", "uf1/~L2hvbWU", true},
		{"/content/tok/ssh1/conn/uf1/5", "tok", "ssh1/conn/uf1/5", true},
		{"/content/tok/uf1/5/", "", "", false},            // a subpath, however short
		{"/content/tok/uf1/5/img/cat.png", "", "", false}, // a subpath
		{"/content/tok/uf1/5/../secret", "", "", false},
		{"/content/tok/uf1/", "", "", false},    // no local id
		{"/content/tok/uf1/abc", "", "", false}, // ditto
		{"/content/tok", "", "", false},         // token alone
		{"/elsewhere/tok/uf1/5", "", "", false}, // wrong prefix
	}
	for _, c := range cases {
		token, tileID, ok := parseContentPath(c.path)
		if token != c.token || tileID != c.tileID || ok != c.ok {
			t.Errorf("parse(%q) = (%q,%q,%v), want (%q,%q,%v)", c.path, token, tileID, ok, c.token, c.tileID, c.ok)
		}
	}
}

// TestPageURLMatchesContentAPIGrammar pins the two sides of the URL contract to
// each other: rpc.PageURL (what the client builds) must parse back through
// parseContentPath (what the API accepts) with nothing lost.
func TestPageURLMatchesContentAPIGrammar(t *testing.T) {
	for _, id := range []string{"uf1/5", "ssh1/conn/uf1/5", "1b467bbd65466256f8a64c538cabdac8/12"} {
		u := rpc.PageURL("https://host:1234", ContentToken("pw"), id)
		path := strings.TrimPrefix(u, "https://host:1234")
		token, tileID, ok := parseContentPath(path)
		if !ok || token != ContentToken("pw") || tileID != id {
			t.Errorf("PageURL(%q) = %q does not round-trip: (%q,%q,%v)", id, u, token, tileID, ok)
		}
	}
}

// contentAPIServer serves a node whose files hold one image, and answers
// the server, a client that carries no cookie, the image tile's qualified id
// and the raw bytes.
func contentAPIServer(t *testing.T) (f *filesServer, bare *http.Client, tileID string, img []byte) {
	t.Helper()
	f = newFilesServer(t)
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	img = buf.Bytes()
	cat := filepath.Join(f.root, "cat.png")
	if err := os.WriteFile(cat, img, 0o644); err != nil {
		t.Fatal(err)
	}
	bare = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return f, bare, rpc.QualifyID(f.node, rpc.KeyTileID(cat)), img
}

// TestContentAPIServesImage: the happy path end to end, WITHOUT any auth
// cookie — the API's own token is the whole credential, because sandboxed
// subresource requests and the desktop's native views can never present the
// cookie.
func TestContentAPIServesImage(t *testing.T) {
	f, bare, tileID, img := contentAPIServer(t)
	hs := f.hs
	token := ContentToken(testPassword)

	res, body := get(t, bare, hs.URL+"/content/"+token+"/"+tileID, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET page = %d %q, want 200", res.StatusCode, body)
	}
	if body != string(img) {
		t.Errorf("body = %d bytes, want the file's %d", len(body), len(img))
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	// The sandbox header is the API's INVARIANT: opaque origin, scripts
	// allowed, no credentials, no reach back into Gridwell. Popups pass, so a
	// target=_blank link opens below like one out of any live page.
	if csp := res.Header.Get("Content-Security-Policy"); csp != "sandbox allow-scripts allow-popups" {
		t.Errorf("CSP = %q, want \"sandbox allow-scripts allow-popups\"", csp)
	}
	if xc := res.Header.Get("X-Content-Type-Options"); xc != "nosniff" {
		t.Errorf("nosniff header = %q", xc)
	}

	// Nothing under the page is a page, and nothing but a read is asked.
	if res, _ := get(t, bare, hs.URL+"/content/"+token+"/"+tileID+"/a.css", ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("GET under the page = %d, want 404", res.StatusCode)
	}
	res, err := bare.Post(hs.URL+"/content/"+token+"/"+tileID, "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", res.StatusCode)
	}
}

// TestContentAPITokenGate: the wrong token — including the AUTH token — is
// a 401; the content token replayed as an auth cookie opens nothing. The
// two derivations live in different domains on purpose.
func TestContentAPITokenGate(t *testing.T) {
	f, bare, tileID, _ := contentAPIServer(t)
	hs := f.hs

	if res, _ := get(t, bare, hs.URL+"/content/"+ContentToken("wrong")+"/"+tileID, ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token = %d, want 401", res.StatusCode)
	}
	if res, _ := get(t, bare, hs.URL+"/content/"+AuthToken(testPassword)+"/"+tileID, ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("auth token as content token = %d, want 401 (domain separation)", res.StatusCode)
	}
	if res, _ := get(t, bare, hs.URL+"/", ContentToken(testPassword)); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("content token as auth cookie = %d, want 401 (domain separation)", res.StatusCode)
	}
}

// TestContentAPIResolvesLeafLink: a home link to an image file serves the
// TARGET's bytes — the API inherits contentRoute, the one resolution
// point, like every other content read.
func TestContentAPIResolvesLeafLink(t *testing.T) {
	f, bare, catID, img := contentAPIServer(t)
	link, err := f.cl.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: f.home, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 0, Y: 0, W: 2, H: 2, LinkTargetId: catID, AltText: "cat.png"}})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	res, body := get(t, bare, f.hs.URL+"/content/"+ContentToken(testPassword)+"/"+link.Id, "")
	if res.StatusCode != http.StatusOK || body != string(img) {
		t.Fatalf("link GET = %d, %d bytes; want the target's %d image bytes", res.StatusCode, len(body), len(img))
	}
}

// TestContentAPIUnimplemented: a namespace that serves no web content
// answers 404, never a 500 — no pages is a normal property of home.
func TestContentAPIUnimplemented(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	_, root := registerPrimaryLocaldb(t, reg, st)
	srv := mustNew(t, reg, Config{})
	hs := serveWeb(t, srv)

	cl := rpc.NewClient(hs.Client(), hs.URL)
	txt, err := cl.CreateWithContent(context.Background(), &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("hi"))
	if err != nil {
		t.Fatal(err)
	}
	res, _ := get(t, noRedirect(hs), hs.URL+"/content/"+ContentToken(testPassword)+"/"+txt.Id, "")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("localdb page GET = %d, want 404 (ServeContent unimplemented)", res.StatusCode)
	}
}

// TestHandshakeCarriesTokensAndRootView pins the Handshake seam facts
// that only ever had producer-side coverage: the content token (must be
// the CONTENT derivation, never the auth token) and a root's persisted
// view — including through a raw BEACON post, the exact wire the unload
// flush uses (application/json unary, no Connect header), so the
// last-pan-survives-quit path rests on a tested seam.
func TestHandshakeCarriesTokensAndRootView(t *testing.T) {
	f, _, _, _ := contentAPIServer(t)
	hs, cl := f.hs, f.cl
	ctx := context.Background()

	pl, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pl.ContentToken != ContentToken(testPassword) {
		t.Errorf("handshake content token = %q, want ContentToken(password)", pl.ContentToken)
	}
	if pl.ContentToken == AuthToken(testPassword) {
		t.Errorf("content token must never equal the auth token (domain separation)")
	}
	// The files' one root is the door this frames: the files row is no
	// place, so it has no root of its own to aim at.
	if len(pl.Plugins[1].PluginGrids) != 1 || pl.Plugins[1].PluginGrids[0].ViewZoom != 0 {
		t.Fatalf("fresh row = %+v, want one grid never visited (zoom 0)", pl.Plugins[1])
	}
	landing := pl.Plugins[1].PluginGrids[0].GridId

	path, body := rpc.SetFramingBeacon(&gridwellv1.SetFramingRequest{
		RootGridId: landing, Cx: 3, Cy: 4, Zoom: 0.5,
	})
	res, err := hs.Client().Post(hs.URL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("beacon-shaped SetFraming POST = %d, want 200", res.StatusCode)
	}
	pl2, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := pl2.Plugins[1].PluginGrids[0]
	if got.ViewCx != 3 || got.ViewCy != 4 || got.ViewZoom != 0.5 {
		t.Errorf("door view after beacon = (%v,%v,%v), want (3,4,0.5)",
			got.ViewCx, got.ViewCy, got.ViewZoom)
	}
}
