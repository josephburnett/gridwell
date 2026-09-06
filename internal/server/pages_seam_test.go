package server

// A plugin whose content IS web pages, through the whole stack: the shipped
// binary spawned by the production loader, the pluginhost adapter, the router,
// and the /content/ door as a browser reaches it. fs proves the door over
// files on disk; this proves the other half — bytes a plugin generates, with
// no file behind them — and it proves the part only the seam can be wrong
// about: a page names its resources with ordinary relative URLs, and what a
// browser does with those URLs must land back on the same plugin with the
// right subpath.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// pagesNS is the registry key for the pages plugin in these tests.
const pagesNS = "up1"

// pagesServer stands the pages plugin up behind the real browser door and
// returns the server plus its root grid.
func pagesServer(t *testing.T) (*httptest.Server, *gridwellv1.GetGridResponse) {
	t.Helper()
	// nil config: the plugin takes none. The loader still hands it uuid, kind
	// and state_dir, which is exactly the shape a config-free plugin must
	// tolerate.
	cl := newPluginClient(t, "pages", nil)
	reg := plugin.NewRegistry()
	reg.Register(pagesNS, "pages", cl, nil)
	hs := httptest.NewServer(mustNew(t, reg, Config{}).WebHandler())
	t.Cleanup(hs.Close)

	ctx := t.Context()
	info, err := cl.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	grid, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	if len(grid.Tiles) == 0 {
		t.Fatal("the pages plugin's root grid is empty")
	}
	return hs, grid
}

// tileByLabel finds a tile by its label.
func tileByLabel(t *testing.T, grid *gridwellv1.GetGridResponse, label string) *gridwellv1.Tile {
	t.Helper()
	for _, tl := range grid.Tiles {
		if tl.AltText == label {
			return tl
		}
	}
	t.Fatalf("no %q tile in %v", label, grid.Tiles)
	return nil
}

// A page tile arrives as a TEXT tile carrying serves_page, with no url of its
// own. That is the shape the whole client rests on: a url tile's own address
// wins over serves_page, so a plugin that declared kind "url" for a page would
// hand the node an address it does not have and the page would never be
// served. The node does not police the combination, so this is where the
// shipped plugin's choice is pinned.
func TestPagesPluginServesPageTilesAsTextRows(t *testing.T) {
	_, grid := pagesServer(t)

	pages := 0
	for _, tl := range grid.Tiles {
		if tl.Kind != rpc.KindText {
			t.Errorf("%s is kind %q; every entry this plugin lists is a text row", tl.AltText, tl.Kind)
		}
		if tl.UrlString != "" {
			t.Errorf("%s carries url %q; a page tile has no address of its own", tl.AltText, tl.UrlString)
		}
		if tl.ServesPage {
			pages++
			if tl.PreviewBlobId == 0 {
				t.Errorf("%s serves a page but declares no face generation", tl.AltText)
			}
		}
	}
	if pages < 2 {
		t.Fatalf("%d page tiles; the plugin exists to demonstrate pages", pages)
	}
	// And the contrast tile is a plain text row, so "text tile" and "page
	// tile" are visibly different things on the same wire shape.
	if about := tileByLabel(t, grid, "about"); about.ServesPage {
		t.Error("the about tile declares serves_page; it is the plain-text contrast")
	}
}

// hrefRe finds the page's relative references, the way a browser would.
var hrefRe = regexp.MustCompile(`(?:src|href)="([^"]+)"`)

// The seam the plugin cannot test alone: the page arrives through the door as
// a whole HTML document, and each relative URL inside it — resolved against
// the tile's address exactly as a browser resolves it — comes back as the
// same tile's ServeContent with that name as the subpath. Nothing on either
// side of the seam can check this: the plugin never sees a URL, and the door
// never sees the HTML.
func TestPagesPluginSubpathsResolveLikeABrowser(t *testing.T) {
	hs, grid := pagesServer(t)
	styled := tileByLabel(t, grid, "styled")
	token := ContentToken(testPassword)

	// Start where the client sends a browser, and let the door's redirect to
	// the trailing slash happen: without it every relative URL below would
	// resolve one level too high.
	start := rpc.PageURL(hs.URL, token, pagesNS+"/"+styled.Id)
	res, body := get(t, hs.Client(), start, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET page = %d %q", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("page Content-Type = %q", ct)
	}
	if !strings.HasPrefix(body, "<!doctype html>") || !strings.Contains(body, "</html>") {
		t.Errorf("the door did not deliver a whole document: %.60q", body)
	}
	// The door's invariant rides a generated page like any other.
	if csp := res.Header.Get("Content-Security-Policy"); csp != "sandbox allow-scripts" {
		t.Errorf("CSP = %q", csp)
	}

	base, err := url.Parse(res.Request.URL.String())
	if err != nil {
		t.Fatal(err)
	}
	refs := 0
	for _, m := range hrefRe.FindAllStringSubmatch(body, -1) {
		ref := m[1]
		if strings.Contains(ref, "://") || strings.HasPrefix(ref, "#") || strings.HasPrefix(ref, "data:") {
			continue
		}
		u, err := url.Parse(ref)
		if err != nil {
			t.Fatalf("page names an unparseable url %q", ref)
		}
		got := base.ResolveReference(u).String()
		// A browser's resolution must land inside this tile's own space, or
		// the request escapes to another tile — or off the door entirely.
		if want := start + ref; got != want {
			t.Errorf("%q resolved to %s, want %s", ref, got, want)
		}
		sub, subBody := get(t, hs.Client(), got, "")
		if sub.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", got, sub.StatusCode)
			continue
		}
		if len(subBody) == 0 {
			t.Errorf("GET %s returned an empty body", got)
		}
		if sub.Header.Get("Content-Type") == "" {
			t.Errorf("GET %s carried no media type; nosniff makes that unusable", got)
		}
		refs++
	}
	if refs < 2 {
		t.Fatalf("the styled page named %d relative resources, want its stylesheet and image", refs)
	}
}

// A subpath the page does not name is an ordinary 404 with a body, not a
// gateway failure: the plugin answers the miss itself, so the status is one
// the plugin chose rather than one the door inferred from an RPC error.
func TestPagesPluginMissingSubpathIs404(t *testing.T) {
	hs, grid := pagesServer(t)
	styled := tileByLabel(t, grid, "styled")
	base := rpc.PageURL(hs.URL, ContentToken(testPassword), pagesNS+"/"+styled.Id)

	res, body := get(t, noRedirect(hs), base+"no-such-asset.css", "")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("missing subpath = %d %q, want 404", res.StatusCode, body)
	}
	if body == "" {
		t.Error("a 404 from the plugin should still say something")
	}
}

// The face travels: a page tile's thumbnail is the plugin's, fetched through
// the same tile the page is, and a tile with no page has none.
func TestPagesPluginFaceComesFromThePlugin(t *testing.T) {
	cl := newPluginClient(t, "pages", nil)
	ctx := t.Context()
	info, err := cl.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	grid, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range grid.Tiles {
		res, err := cl.GetTilePreview(ctx, &gridwellv1.GetTilePreviewRequest{TileId: tl.Id})
		if err != nil {
			t.Fatalf("preview of %s: %v", tl.AltText, err)
		}
		if tl.ServesPage && len(res.GetJpeg()) == 0 {
			t.Errorf("%s serves a page but has no face", tl.AltText)
		}
		if !tl.ServesPage && len(res.GetJpeg()) != 0 {
			t.Errorf("%s is a text tile with a thumbnail", tl.AltText)
		}
	}
}
