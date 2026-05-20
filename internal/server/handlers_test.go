package server

import (
	"net/http"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// largeView returns a viewrect that contains anything reasonable.
func largeView() rpc.ViewRect { return rpc.ViewRect{X: -100, Y: -100, W: 200, H: 200} }

// createWell helper: creates a well via the RPC layer at (x, y) and returns
// the response.
func createWell(t *testing.T, hs *struct{ URL string }, cookie *http.Cookie, gridID int64, x, y int64) rpc.Tile {
	t.Helper()
	t.Fatal("unused — use the structured server tests instead")
	return rpc.Tile{}
}

// silence unused param.
var _ = createWell

func TestCreateFileHappyPath(t *testing.T) {
	hs, u, cookie := newTestServer(t)
	var nr rpc.TileResponse
	st, body := callRPC(t, hs, cookie, "CreateFile", &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID,
		X: 1, Y: 1, W: 1, H: 1, MimeType: "text/markdown", Data: []byte("# hi"),
	}, &nr)
	if st != 200 {
		t.Fatalf("status %d: %s", st, body)
	}
	if nr.Tile.Type != "file" || nr.Tile.MimeType != "text/markdown" {
		t.Errorf("got %+v", nr.Tile)
	}

	// GetBlob should return the bytes.
	var br rpc.GetBlobResponse
	st, body = callRPC(t, hs, cookie, "GetBlob", &rpc.GetBlobRequest{BlobID: nr.Tile.BlobID}, &br)
	if st != 200 {
		t.Fatalf("blob status %d: %s", st, body)
	}
	if string(br.Data) != "# hi" {
		t.Errorf("blob data = %q", br.Data)
	}
}

func TestResizeAndViewportRPCs(t *testing.T) {
	hs, u, cookie := newTestServer(t)
	var nr rpc.TileResponse
	if st, _ := callRPC(t, hs, cookie, "CreateWell", &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	}, &nr); st != 200 {
		t.Fatal("create")
	}
	id := nr.Tile.ID

	// Resize.
	if st, body := callRPC(t, hs, cookie, "ResizeTile", &rpc.ResizeTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: id, W: 2, H: 2,
	}, &nr); st != 200 {
		t.Fatalf("resize: %d %s", st, body)
	}
	// Set viewport.
	if st, body := callRPC(t, hs, cookie, "SetTileViewport", &rpc.SetTileViewportRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: id, ViewX: 7, ViewY: 8,
	}, &nr); st != 200 {
		t.Fatalf("viewport: %d %s", st, body)
	}
	if nr.Tile.ViewX != 7 || nr.Tile.ViewY != 8 {
		t.Errorf("after viewport: %+v", nr.Tile)
	}
}

func TestDeleteTileRPC(t *testing.T) {
	hs, u, cookie := newTestServer(t)
	var nr rpc.TileResponse
	if st, _ := callRPC(t, hs, cookie, "CreateWell", &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	}, &nr); st != 200 {
		t.Fatal("create")
	}
	id := nr.Tile.ID

	var dr rpc.DeleteTileResponse
	if st, body := callRPC(t, hs, cookie, "DeleteTile", &rpc.DeleteTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: id,
	}, &dr); st != 200 {
		t.Fatalf("delete: %d %s", st, body)
	}
}

func TestAscendAtRootRPC(t *testing.T) {
	hs, _, cookie := newTestServer(t)
	var ar rpc.AscendAtRootResponse
	if st, body := callRPC(t, hs, cookie, "AscendAtRoot", &rpc.AscendAtRootRequest{}, &ar); st != 200 {
		t.Fatalf("ascend: %d %s", st, body)
	}
	if ar.NewRootGridID == 0 || ar.WellID == 0 {
		t.Errorf("response empty: %+v", ar)
	}
}

func TestUpdateFileContentRPC(t *testing.T) {
	hs, u, cookie := newTestServer(t)
	var nr rpc.TileResponse
	if st, _ := callRPC(t, hs, cookie, "CreateFile", &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID,
		X: 0, Y: 0, W: 1, H: 1, MimeType: "text/markdown", Data: []byte("v1"),
	}, &nr); st != 200 {
		t.Fatal("create file")
	}
	id := nr.Tile.ID

	if st, body := callRPC(t, hs, cookie, "UpdateFileContent", &rpc.UpdateFileContentRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: id, Data: []byte("v2"),
	}, &nr); st != 200 {
		t.Fatalf("update: %d %s", st, body)
	}
}

func TestCloneAndMoveRPCs(t *testing.T) {
	hs, u, cookie := newTestServer(t)
	var nr rpc.TileResponse
	if st, _ := callRPC(t, hs, cookie, "CreateWell", &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	}, &nr); st != 200 {
		t.Fatal("create")
	}
	src := nr.Tile.ID

	// Clone.
	if st, body := callRPC(t, hs, cookie, "CloneTile", &rpc.CloneTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: src,
		DestGridID: u.RootGridID, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 5, Y: 5,
	}, &nr); st != 200 {
		t.Fatalf("clone: %d %s", st, body)
	}
	clone := nr.Tile.ID

	// Move clone.
	var mr rpc.MoveTileResponse
	if st, body := callRPC(t, hs, cookie, "MoveTile", &rpc.MoveTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: clone,
		DestGridID: u.RootGridID, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 8, Y: 8,
	}, &mr); st != 200 {
		t.Fatalf("move: %d %s", st, body)
	}
	if mr.Tile.X != 8 || mr.Tile.Y != 8 {
		t.Errorf("moved to %+v", mr.Tile)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	hs, _, cookie := newTestServer(t)
	r, _ := http.NewRequest(http.MethodGet, hs.URL+"/rpc/Whoami", nil)
	r.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestErrorStatusMapping(t *testing.T) {
	hs, u, cookie := newTestServer(t)
	// Locality refusal: footprint outside view rect.
	st, _ := callRPC(t, hs, cookie, "CreateWell", &rpc.CreateWellRequest{
		Path:     rpc.Path{},
		ViewRect: rpc.ViewRect{X: 0, Y: 0, W: 1, H: 1},
		GridID:   u.RootGridID, X: 5, Y: 5, W: 1, H: 1,
	}, nil)
	if st != http.StatusConflict {
		t.Errorf("locality refused: status %d, want 409", st)
	}

	// Invalid path: well doesn't exist.
	st, _ = callRPC(t, hs, cookie, "CreateWell", &rpc.CreateWellRequest{
		Path:     rpc.Path{WellIDs: []int64{99}},
		ViewRect: largeView(),
		GridID:   1, X: 0, Y: 0, W: 1, H: 1,
	}, nil)
	if st != http.StatusBadRequest {
		t.Errorf("invalid path: status %d, want 400", st)
	}

	// Unsupported mime.
	st, _ = callRPC(t, hs, cookie, "CreateFile", &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID,
		X: 0, Y: 0, W: 1, H: 1, MimeType: "application/x-evil", Data: []byte("x"),
	}, nil)
	if st != http.StatusBadRequest {
		t.Errorf("bad mime: status %d, want 400", st)
	}
}

