package pluginhost_test

// A plugin url tile against a home one: the screenshot, the standing freeze
// and the zoom are the node's memory of how the user left the tile, whoever
// serves the page, so each rides the same SetTile arms home's does. The one
// difference is the address, which is the plugin's: a writeback naming one is
// refused. The shipped fs binary serves the pages, through a full server.

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// countingCP counts the plugin's GetPreview calls, the question a stored
// screenshot must stop the node from asking.
type countingCP struct {
	pluginv1.PluginClient
	previews atomic.Int32
}

func (c *countingCP) GetPreview(ctx context.Context, req *pluginv1.GetPreviewRequest, opts ...grpc.CallOption) (*pluginv1.GetPreviewResponse, error) {
	c.previews.Add(1)
	return c.PluginClient.GetPreview(ctx, req, opts...)
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// faceNode serves an fs root holding one html page and one image, and hands
// back the client, the landing grid and the plugin's preview counter.
func faceNode(t *testing.T) (*rpc.Client, string, *countingCP) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "page.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pic.png"), pngBytes(t), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cp := &countingCP{PluginClient: plugintest.Spawn(t, "fs", map[string]string{"root": root})}
	reg := plugin.NewRegistry()
	reg.Register(fsUUID, "fs", pluginhost.New(cp, st.Namespace("p1"), nil), nil)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	pl, err := cl.Handshake(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cl, plugintest.LandingOf(t, pl.Plugins[0]), cp
}

func faceOf(t *testing.T, cl *rpc.Client, grid, label string) *gridwellv1.Tile {
	t.Helper()
	g, err := cl.GetGrid(context.Background(), grid)
	if err != nil {
		t.Fatal(err)
	}
	if tile := tileNamed(g.Tiles, label); tile != nil {
		return tile
	}
	t.Fatalf("no %q in the listing", label)
	return nil
}

func TestAPluginPageKeepsTheScreenshotItWasLeftWith(t *testing.T) {
	cl, grid, cp := faceNode(t)
	ctx := context.Background()
	page := faceOf(t, cl, grid, "page.html")
	if !rpc.PageContent(page) {
		t.Fatalf("page.html is not a served page: %+v", page)
	}
	if page.PreviewBlobId != 0 {
		t.Fatalf("an unvisited page with no plugin picture keys a face: %d", page.PreviewBlobId)
	}

	shot := []byte("\xff\xd8screenshot")
	if _, err := cl.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: page.Id,
		Tile: &gridwellv1.Tile{Kind: rpc.KindURL}, Preview: shot}); err != nil {
		t.Fatalf("the screenshot of a plugin page was refused: %v", err)
	}
	before := cp.previews.Load()
	got, err := cl.GetTilePreview(ctx, page.Id)
	if err != nil || !bytes.Equal(got, shot) {
		t.Fatalf("GetTilePreview = %q (%v), want the screenshot", got, err)
	}
	if n := cp.previews.Load() - before; n != 0 {
		t.Fatalf("the plugin was asked for a face the node holds (%d calls)", n)
	}
	if k := faceOf(t, cl, grid, "page.html").PreviewBlobId; k <= 0 {
		t.Fatalf("the listing does not key the screenshot: %d", k)
	}
}

func TestAPluginPictureIsTheFaceOnlyUntilTheFirstScreenshot(t *testing.T) {
	cl, grid, cp := faceNode(t)
	ctx := context.Background()
	pic := faceOf(t, cl, grid, "pic.png")
	if pic.PreviewBlobId >= 0 {
		t.Fatalf("a plugin picture keys %d, want the negated stamp", pic.PreviewBlobId)
	}
	before := cp.previews.Load()
	thumb, err := cl.GetTilePreview(ctx, pic.Id)
	if err != nil || len(thumb) == 0 {
		t.Fatalf("the plugin's thumbnail did not arrive: %v", err)
	}
	if cp.previews.Load() == before {
		t.Fatal("the plugin was not asked for its picture")
	}

	shot := []byte("\xff\xd8screenshot")
	if _, err := cl.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: pic.Id,
		Tile: &gridwellv1.Tile{Kind: rpc.KindURL}, Preview: shot}); err != nil {
		t.Fatal(err)
	}
	got, err := cl.GetTilePreview(ctx, pic.Id)
	if err != nil || !bytes.Equal(got, shot) {
		t.Fatalf("GetTilePreview = %d bytes (%v), want the screenshot", len(got), err)
	}
	if k := faceOf(t, cl, grid, "pic.png").PreviewBlobId; k <= 0 {
		t.Fatalf("the screenshot does not win the face key: %d", k)
	}
}

func TestAPluginPagesAddressIsNotTheWritebacksToSay(t *testing.T) {
	cl, grid, _ := faceNode(t)
	ctx := context.Background()
	page := faceOf(t, cl, grid, "page.html")
	for name, tile := range map[string]*gridwellv1.Tile{
		"url_string":  {Kind: rpc.KindURL, UrlString: "http://elsewhere.test/"},
		"alt_text":    {Kind: rpc.KindURL, AltText: "a page title"},
		"url_history": {Kind: rpc.KindURL, UrlHistory: `{"entries":[]}`},
	} {
		_, err := cl.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: page.Id, Tile: tile, Preview: []byte("x")})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("a writeback carrying %s = %v, want InvalidArgument", name, err)
		}
	}
	if faceOf(t, cl, grid, "page.html").PreviewBlobId != 0 {
		t.Fatal("a refused writeback still stored its screenshot")
	}
}

func TestAPluginPageHoldsTheStandingFreeze(t *testing.T) {
	cl, grid, _ := faceNode(t)
	ctx := context.Background()
	page := faceOf(t, cl, grid, "page.html")
	for _, want := range []bool{true, false} {
		if _, err := cl.SetFrozen(ctx, page.Id, want); err != nil {
			t.Fatalf("SetFrozen(%v) on a plugin page: %v", want, err)
		}
		if got := faceOf(t, cl, grid, "page.html").UrlFrozen; got != want {
			t.Fatalf("url_frozen = %v, want %v", got, want)
		}
	}
}
