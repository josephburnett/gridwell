package server

// A plugin whose content IS web pages, through the whole stack: the shipped
// binary spawned by the production loader, the pluginhost adapter, the router,
// and the /content/ API as a browser reaches it. fs proves the API over
// files on disk; this proves the other half — bytes a plugin generates, with
// no file behind them — and it proves the part only the seam can be wrong
// about: a page names its resources with ordinary relative URLs, and what a
// browser does with those URLs must land back on the same plugin with the
// right subpath.

import (
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/claimtest"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// pagesNS is the registry key for the pages plugin in these tests.
const pagesNS = "up1"

// pagesServer stands the pages plugin up behind the real web API and
// returns the server plus its root grid.
func pagesServer(t *testing.T) (*httptest.Server, *gridwellv1.GetGridResponse) {
	t.Helper()
	// nil config: the plugin takes none. The loader still hands it uuid, kind
	// and state_dir, which is exactly the shape a config-free plugin must
	// tolerate.
	cl := newPluginClient(t, "pages", nil)
	reg := plugin.NewRegistry()
	reg.Register(pagesNS, "pages", cl, nil)
	hs := webAPITest(t, mustNew(t, reg, Config{}).WebHandler())

	ctx := t.Context()
	info, err := cl.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	grid, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: plugintest.Landing(t, info)})
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

// A page tile arrives as a URL tile carrying serves_page, with no url of its
// own: the address is the node's to derive at the /content/ API. The entry
// API refuses that declaration on every other kind, and this is where the
// shipped plugin's side of it is pinned.
func TestPagesPluginServesPageTilesAsURLRows(t *testing.T) {
	_, grid := pagesServer(t)

	pages := 0
	for _, tl := range grid.Tiles {
		if tl.UrlString != "" {
			t.Errorf("%s carries url %q; the node derives a served page's address", tl.AltText, tl.UrlString)
		}
		if !tl.ServesPage {
			continue
		}
		pages++
		if tl.Kind != rpc.KindURL {
			t.Errorf("%s is kind %q; a page is served from a url row", tl.AltText, tl.Kind)
		}
		if tl.PreviewBlobId == 0 {
			t.Errorf("%s serves a page but declares no preview generation", tl.AltText)
		}
	}
	if pages < 2 {
		t.Fatalf("%d page tiles; the plugin exists to demonstrate pages", pages)
	}
	// And the contrast tile is a plain text row, so a page and a document are
	// two kinds rather than one kind flagged two ways.
	if about := tileByLabel(t, grid, "about"); about.ServesPage || about.Kind != rpc.KindText {
		t.Errorf("the about tile = kind %q serves_page %v; it is the plain-text contrast",
			about.Kind, about.ServesPage)
	}
}

// The preview travels: a page tile's thumbnail is the plugin's, fetched through
// the same tile the page is, and a tile with no page has none.
func TestPagesPluginPreviewComesFromThePlugin(t *testing.T) {
	cl := newPluginClient(t, "pages", nil)
	ctx := t.Context()
	info, err := cl.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	grid, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: plugintest.Landing(t, info)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range grid.Tiles {
		res, err := cl.GetTilePreview(ctx, &gridwellv1.GetTilePreviewRequest{TileId: tl.Id})
		if err != nil {
			t.Fatalf("preview of %s: %v", tl.AltText, err)
		}
		if tl.ServesPage && len(res.GetJpeg()) == 0 {
			t.Errorf("%s serves a page but has no preview", tl.AltText)
		}
		if !tl.ServesPage && len(res.GetJpeg()) != 0 {
			t.Errorf("%s is a text tile with a thumbnail", tl.AltText)
		}
	}
}

// The text tile reads through the node as the plugin's markdown, and declares
// it so: a body the client renders as plain text would show the markup.
func TestPagesPluginTextTileIsMarkdown(t *testing.T) {
	hs, grid := pagesServer(t)
	about := tileByLabel(t, grid, "about")
	if about.TextPresentation != rpc.TextPresentationBoth {
		t.Errorf("about text_presentation = %q, want %q", about.TextPresentation, rpc.TextPresentationBoth)
	}
	data, mediaType, _, err := pagesRPC(t, hs).ReadContent(t.Context(), pagesNS+"/"+about.Id)
	if err != nil {
		t.Fatal(err)
	}
	if mediaType != "text/markdown" || !strings.Contains(string(data), "# pages") {
		t.Errorf("about = %q %.40q, want the markdown note", mediaType, data)
	}
}

// The node's Probe of a listed tile asks the plugin in the grid the tile
// was listed in, and the plugin answers PRESENT there: a grid either side
// gets wrong reads as GONE, and GONE retires a live tile.
func TestPagesPluginProbesAListedTilePresent(t *testing.T) {
	cl := newPluginClient(t, "pages", nil)
	ctx := t.Context()
	info, err := cl.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	grid, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: plugintest.Landing(t, info)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range grid.Tiles {
		got, err := cl.Probe(ctx, &gridwellv1.ProbeRequest{TileId: tl.Id})
		if err != nil || got.Presence != gridwellv1.ProbeResponse_PRESENCE_PRESENT {
			t.Errorf("Probe(%s) = %v, %v; want PRESENT", tl.AltText, got.GetPresence(), err)
		}
	}
}

// A delete of a fixed page is refused, and the refusal reaches the caller of
// the web API with its code and its reason; the tile stays listed under the
// same id, because nothing was deleted.
func TestPagesPluginDeleteIsRefusedWithItsReason(t *testing.T) {
	hs, grid := pagesServer(t)
	hello := tileByLabel(t, grid, "hello")
	cl := pagesRPC(t, hs)
	ctx := t.Context()

	_, err := cl.DeleteTile(ctx, claimtest.Claimed(t, cl, &gridwellv1.DeleteTileRequest{TileId: pagesNS + "/" + hello.Id}))
	if connect.CodeOf(err) != connect.CodeUnimplemented || !strings.Contains(err.Error(), "fixed") {
		t.Fatalf("DeleteTile = %v, want Unimplemented saying the site is fixed", err)
	}
	after, err := cl.GetGrid(ctx, pagesNS+"/"+hello.GridId)
	if err != nil {
		t.Fatal(err)
	}
	if again := tileByLabel(t, after, "hello"); again.Id != pagesNS+"/"+hello.Id {
		t.Errorf("after a refused delete hello is %s, want the same id %s", again.Id, hello.Id)
	}
}

// pagesRPC is a logged-in connect client on the pages server's web API.
func pagesRPC(t *testing.T, hs *httptest.Server) *rpc.Client {
	t.Helper()
	return rpc.NewClient(withCookie(t, hs, testPassword).Client(), hs.URL, connect.WithProtoJSON())
}
