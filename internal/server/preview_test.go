package server

import (
	"bytes"
	"context"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

func getPreview(t *testing.T, hs *httptest.Server, tileID string, w, h int) (int, string, []byte) {
	t.Helper()
	url := hs.URL + "/preview/tile/" + tileID +
		"?w=" + strconv.Itoa(w) + "&h=" + strconv.Itoa(h)
	got, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(got.Body)
	return got.StatusCode, got.Header.Get("Content-Type"), buf.Bytes()
}

func TestPreviewTilePlaceholderText(t *testing.T) {
	hs, cl, root := newTestServer(t)
	tile, err := cl.CreateText(context.Background(), &rpc.CreateTextRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("# hi"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	st, ct, body := getPreview(t, hs, tile.ID, 64, 64)
	if st != 200 {
		t.Fatalf("preview status %d body=%s", st, body)
	}
	if ct != "image/png" {
		t.Errorf("content-type = %q, want image/png", ct)
	}
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 64 {
		t.Errorf("dims = %dx%d, want 64x64", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestPreviewTilePlaceholderWell(t *testing.T) {
	hs, cl, root := newTestServer(t)
	tile, err := cl.CreateWell(context.Background(), &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatalf("create well: %v", err)
	}
	st, ct, _ := getPreview(t, hs, tile.ID, 128, 64)
	if st != 200 || ct != "image/png" {
		t.Fatalf("status=%d ct=%q", st, ct)
	}
}

func TestPreviewTileURLNoJPEGFallsBack(t *testing.T) {
	hs, cl, root := newTestServer(t)
	tile, err := cl.CreateURL(context.Background(), &rpc.CreateURLRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, URL: "https://example.com",
	})
	if err != nil {
		t.Fatalf("create url: %v", err)
	}
	// No live session has run; preview is empty. The endpoint falls
	// back to the kind-colored placeholder, not 404.
	st, ct, _ := getPreview(t, hs, tile.ID, 96, 96)
	if st != 200 {
		t.Fatalf("status=%d", st)
	}
	if ct != "image/png" {
		t.Errorf("content-type = %q, want image/png (fallback)", ct)
	}
}

func TestPreviewTileBadID(t *testing.T) {
	hs, _, _ := newTestServer(t)
	got, err := http.Get(hs.URL + "/preview/tile/notanumber")
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	if got.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", got.StatusCode)
	}
}

func TestPreviewTileNotFound(t *testing.T) {
	hs, _, root := newTestServer(t)
	// A qualified id in the (registered) primary plugin namespace with a bogus
	// local id → routes to the plugin → not found.
	uuid, _, _ := splitPluginID(root)
	got, err := http.Get(hs.URL + "/preview/tile/" + uuid + "/999999")
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	if got.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", got.StatusCode)
	}
}

func TestPreviewSizeClamp(t *testing.T) {
	hs, cl, root := newTestServer(t)
	tile, err := cl.CreateText(context.Background(), &rpc.CreateTextRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("hi"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Empty/zero → default 192x128.
	st, _, body := getPreview(t, hs, tile.ID, 0, 0)
	if st != 200 {
		t.Fatalf("status %d", st)
	}
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 192 || img.Bounds().Dy() != 128 {
		t.Errorf("default dims = %dx%d, want 192x128", img.Bounds().Dx(), img.Bounds().Dy())
	}

	// Above clamp → 2048.
	st, _, body = getPreview(t, hs, tile.ID, 10000, 10000)
	if st != 200 {
		t.Fatalf("status %d", st)
	}
	img, err = png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 2048 || img.Bounds().Dy() != 2048 {
		t.Errorf("clamped dims = %dx%d, want 2048x2048", img.Bounds().Dx(), img.Bounds().Dy())
	}
}
