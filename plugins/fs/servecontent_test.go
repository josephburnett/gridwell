package fs_test

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/plugins/fs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The fs half of the web-content seam (2026-08-11): ServeContent must serve
// a file's REAL bytes with a real media type over a real gRPC stream —
// unlike ReadContent, whose body is the small document view — and the
// serves_page bit must ride the tile rows so the client knows to present
// the descent as web content.

func servePage(t *testing.T, client gridwellv1.GridwellClient, tileID, subpath string) (status int64, mediaType string, body []byte) {
	t.Helper()
	stream, err := client.ServeContent(context.Background(), &gridwellv1.ServeContentRequest{TileId: tileID, Subpath: subpath})
	if err != nil {
		t.Fatalf("ServeContent: %v", err)
	}
	first := true
	for {
		ch, err := stream.Recv()
		if err == io.EOF {
			return status, mediaType, body
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if first {
			status, mediaType, first = ch.Status, ch.MediaType, false
		}
		body = append(body, ch.Data...)
	}
}

func fsRootGrid(t *testing.T, dir string) (gridwellv1.GridwellClient, *gridwellv1.GetGridResponse) {
	t.Helper()
	p, err := fs.Open(":memory:", nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	p.SetRoot(dir)
	client, closer, err := compose.ServeInProcess(p)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	t.Cleanup(closer)
	info, err := client.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	grid, err := client.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatalf("grid: %v", err)
	}
	return client, grid
}

func tileNamed(t *testing.T, grid *gridwellv1.GetGridResponse, name string) *gridwellv1.Tile {
	t.Helper()
	for _, tl := range grid.Tiles {
		if tl.AltText == name {
			return tl
		}
	}
	t.Fatalf("no tile named %q in %v", name, grid.Tiles)
	return nil
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// An image file: serves_page rides the tile row, and ServeContent streams
// the file's own bytes with its real media type. This is "image viewing":
// the descent presents the picture, not the metadata summary.
func TestFSImageServesPage(t *testing.T) {
	dir := t.TempDir()
	img := pngBytes(t, 3, 2)
	if err := os.WriteFile(filepath.Join(dir, "cat.png"), img, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	client, grid := fsRootGrid(t, dir)

	cat := tileNamed(t, grid, "cat.png")
	if !cat.ServesPage {
		t.Error("cat.png tile must declare serves_page")
	}
	if md := tileNamed(t, grid, "notes.md"); md.ServesPage {
		t.Error("notes.md must NOT declare serves_page (it has a document descent)")
	}
	// GetTile agrees with GetGrid — same derivation, both doors.
	got, err := client.GetTile(context.Background(), &gridwellv1.GetTileRequest{TileId: cat.Id})
	if err != nil {
		t.Fatalf("GetTile: %v", err)
	}
	if !got.Tile.ServesPage {
		t.Error("GetTile must stamp serves_page like GetGrid does")
	}

	st, mt, body := servePage(t, client, cat.Id, "")
	if st != 200 || mt != "image/png" || !bytes.Equal(body, img) {
		t.Errorf("root page = (%d, %q, %d bytes), want (200, image/png, the file's %d bytes)", st, mt, len(body), len(img))
	}
}

// A whole webpage: an HTML file serves as a page and its RELATIVE
// subresources resolve against the page's own directory — and never
// outside it.
func TestFSHTMLPageWithSubresources(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "site", "img"), 0o755); err != nil {
		t.Fatal(err)
	}
	page := []byte(`<h1>hello</h1><img src="img/cat.png">`)
	img := pngBytes(t, 1, 1)
	secret := []byte("secret")
	if err := os.WriteFile(filepath.Join(dir, "site", "index.html"), page, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "site", "img", "cat.png"), img, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "outside.txt"), secret, 0o644); err != nil {
		t.Fatal(err)
	}
	client, root := fsRootGrid(t, dir)
	site := tileNamed(t, root, "site")
	sub, err := client.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: site.ChildGridId})
	if err != nil {
		t.Fatalf("site grid: %v", err)
	}
	index := tileNamed(t, sub, "index.html")
	if !index.ServesPage {
		t.Error("index.html must declare serves_page")
	}

	st, mt, body := servePage(t, client, index.Id, "")
	if st != 200 || !strings.HasPrefix(mt, "text/html") || !bytes.Equal(body, page) {
		t.Errorf("page = (%d, %q), want the html itself", st, mt)
	}
	st, mt, body = servePage(t, client, index.Id, "img/cat.png")
	if st != 200 || mt != "image/png" || !bytes.Equal(body, img) {
		t.Errorf("subresource = (%d, %q, %d bytes), want (200, image/png, %d bytes)", st, mt, len(body), len(img))
	}
	// Confinement is the PLUGIN'S invariant, not just the HTTP door's URL
	// grammar: a subpath that resolves outside the page's directory answers
	// 404 and never the file.
	st, _, body = servePage(t, client, index.Id, "../outside.txt")
	if st != 404 || bytes.Contains(body, secret) {
		t.Errorf("escaping subpath = (%d, %q), want a 404 without the bytes", st, body)
	}
	// A missing subresource is a 404 PAGE (a plugin answer), not an error.
	if st, _, _ := servePage(t, client, index.Id, "img/missing.png"); st != 404 {
		t.Errorf("missing subresource status = %d, want 404", st)
	}
}

// Any file serves raw through the door — serves_page only decides the
// DESCENT presentation. Directories serve nothing.
func TestFSServeContentEdges(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A file big enough to need more than one 256 KiB chunk.
	big := bytes.Repeat([]byte("x"), 300*1024)
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	client, grid := fsRootGrid(t, dir)

	plain := tileNamed(t, grid, "plain.txt")
	if plain.ServesPage {
		t.Error("plain.txt must not declare serves_page")
	}
	st, mt, body := servePage(t, client, plain.Id, "")
	if st != 200 || !strings.HasPrefix(mt, "text/plain") || string(body) != "plain" {
		t.Errorf("txt = (%d, %q, %q)", st, mt, body)
	}

	if st, _, body := servePage(t, client, tileNamed(t, grid, "big.bin").Id, ""); st != 200 || !bytes.Equal(body, big) {
		t.Errorf("big file did not stream whole: status %d, %d bytes of %d", st, len(body), len(big))
	}

	well := tileNamed(t, grid, "sub")
	if _, err := func() (any, error) {
		stream, err := client.ServeContent(context.Background(), &gridwellv1.ServeContentRequest{TileId: well.Id})
		if err != nil {
			return nil, err
		}
		_, err = stream.Recv()
		return nil, err
	}(); status.Code(err) != codes.NotFound {
		t.Errorf("directory ServeContent = %v, want NotFound", err)
	}
}

// The frozen face: GetTilePreview on an image file returns a bounded JPEG
// thumbnail; everything else stays empty (falls back to the label).
func TestFSImagePreview(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wide.png"), pngBytes(t, 1200, 300), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	client, grid := fsRootGrid(t, dir)

	resp, err := client.GetTilePreview(context.Background(), &gridwellv1.GetTilePreviewRequest{
		TileId: tileNamed(t, grid, "wide.png").Id,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(resp.Jpeg))
	if err != nil {
		t.Fatalf("preview is not a decodable jpeg: %v", err)
	}
	if w := img.Bounds().Dx(); w != 512 {
		t.Errorf("thumbnail width = %d, want bounded to 512", w)
	}
	if h := img.Bounds().Dy(); h != 128 {
		t.Errorf("thumbnail height = %d, want 128 (aspect kept)", h)
	}

	md, err := client.GetTilePreview(context.Background(), &gridwellv1.GetTilePreviewRequest{
		TileId: tileNamed(t, grid, "notes.md").Id,
	})
	if err != nil {
		t.Fatalf("md preview: %v", err)
	}
	if len(md.Jpeg) != 0 {
		t.Errorf("non-image preview = %d bytes, want empty", len(md.Jpeg))
	}
}
