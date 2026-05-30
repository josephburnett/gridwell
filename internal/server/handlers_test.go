package server

import (
	"net/http"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

func TestCreateTextRPC(t *testing.T) {
	hs, root := newTestServer(t)
	var nr rpc.TileResponse
	st, body := callRPC(t, hs, "CreateText", &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root,
		X: 1, Y: 1, W: 1, H: 1, Data: []byte("# hi"),
	}, &nr)
	if st != 200 {
		t.Fatalf("status %d: %s", st, body)
	}
	if nr.Tile.Kind != rpc.KindText {
		t.Errorf("got kind %q, want %q", nr.Tile.Kind, rpc.KindText)
	}
	if nr.Tile.BlobID == 0 {
		t.Errorf("blob_id = 0, want non-zero")
	}

	var br rpc.GetBlobResponse
	st, body = callRPC(t, hs, "GetBlob", &rpc.GetBlobRequest{BlobID: nr.Tile.BlobID}, &br)
	if st != 200 {
		t.Fatalf("blob status %d: %s", st, body)
	}
	if string(br.Data) != "# hi" {
		t.Errorf("blob data = %q", br.Data)
	}
}

func TestCreateURLRPC(t *testing.T) {
	hs, root := newTestServer(t)
	var nr rpc.TileResponse
	st, body := callRPC(t, hs, "CreateURL", &rpc.CreateURLRequest{
		Path: rpc.Path{}, GridID: root,
		X: 0, Y: 0, W: 1, H: 1, URL: "https://example.com",
	}, &nr)
	if st != 200 {
		t.Fatalf("status %d: %s", st, body)
	}
	if nr.Tile.Kind != rpc.KindURL {
		t.Errorf("got kind %q, want %q", nr.Tile.Kind, rpc.KindURL)
	}
	if nr.Tile.URLString != "https://example.com" {
		t.Errorf("url_string = %q", nr.Tile.URLString)
	}
}

func TestCreateBlackHoleRPC(t *testing.T) {
	hs, root := newTestServer(t)
	var nr rpc.TileResponse
	st, body := callRPC(t, hs, "CreateBlackHole", &rpc.CreateBlackHoleRequest{
		Path: rpc.Path{}, GridID: root,
		X: 2, Y: 2, W: 1, H: 1,
	}, &nr)
	if st != 200 {
		t.Fatalf("status %d: %s", st, body)
	}
	if nr.Tile.Kind != rpc.KindBlackHole {
		t.Errorf("got kind %q, want %q", nr.Tile.Kind, rpc.KindBlackHole)
	}
}

func TestResizeAndSetWellViewRPCs(t *testing.T) {
	hs, root := newTestServer(t)
	var nr rpc.TileResponse
	if st, _ := callRPC(t, hs, "CreateWell", &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	}, &nr); st != 200 {
		t.Fatal("create")
	}
	id := nr.Tile.ID
	v := nr.Tile.Version

	if st, body := callRPC(t, hs, "ResizeTile", &rpc.ResizeTileRequest{
		Path: rpc.Path{}, TileID: id, Version: v, X: 0, Y: 0, W: 2, H: 2,
	}, &nr); st != 200 {
		t.Fatalf("resize: %d %s", st, body)
	}
	v = nr.Tile.Version

	if st, body := callRPC(t, hs, "SetWellView", &rpc.SetWellViewRequest{
		Path: rpc.Path{}, TileID: id, Version: v, ViewX: 7, ViewY: 8, ViewZoom: 1.5,
	}, &nr); st != 200 {
		t.Fatalf("set well view: %d %s", st, body)
	}
	if nr.Tile.ViewX != 7 || nr.Tile.ViewY != 8 || nr.Tile.ViewZoom != 1.5 {
		t.Errorf("after set well view: %+v", nr.Tile)
	}
}

func TestSetTextViewRPC(t *testing.T) {
	hs, root := newTestServer(t)
	var nr rpc.TileResponse
	if st, body := callRPC(t, hs, "CreateText", &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("hi"),
	}, &nr); st != 200 {
		t.Fatalf("create text: %d %s", st, body)
	}
	id := nr.Tile.ID
	v := nr.Tile.Version

	if st, body := callRPC(t, hs, "SetTextView", &rpc.SetTextViewRequest{
		Path: rpc.Path{}, TileID: id, Version: v,
		TextX: 1, TextY: 2, TextW: 3, TextH: 4, TextMode: rpc.TextModeRendered,
	}, &nr); st != 200 {
		t.Fatalf("set text view: %d %s", st, body)
	}
	if nr.Tile.TextX != 1 || nr.Tile.TextY != 2 || nr.Tile.TextW != 3 || nr.Tile.TextH != 4 {
		t.Errorf("after set text view: %+v", nr.Tile)
	}
	if nr.Tile.TextMode != rpc.TextModeRendered {
		t.Errorf("text_mode = %q, want %q", nr.Tile.TextMode, rpc.TextModeRendered)
	}
}

func TestSetRootViewRPC(t *testing.T) {
	hs, _ := newTestServer(t)
	var resp rpc.SetRootViewResponse
	if st, body := callRPC(t, hs, "SetRootView", &rpc.SetRootViewRequest{
		Cx: 3, Cy: 4, Zoom: 2,
	}, &resp); st != 200 {
		t.Fatalf("set root view: %d %s", st, body)
	}

	var br rpc.BootstrapResponse
	if st, body := callRPC(t, hs, "Bootstrap", &rpc.BootstrapRequest{}, &br); st != 200 {
		t.Fatalf("bootstrap: %d %s", st, body)
	}
	if br.RootViewCx != 3 || br.RootViewCy != 4 || br.RootZoom != 2 {
		t.Errorf("after SetRootView, bootstrap = %+v", br)
	}
}

func TestDeleteTileRPC(t *testing.T) {
	hs, root := newTestServer(t)
	var nr rpc.TileResponse
	if st, _ := callRPC(t, hs, "CreateWell", &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	}, &nr); st != 200 {
		t.Fatal("create")
	}
	id := nr.Tile.ID
	v := nr.Tile.Version

	var dr rpc.DeleteTileResponse
	if st, body := callRPC(t, hs, "DeleteTile", &rpc.DeleteTileRequest{
		Path: rpc.Path{}, TileID: id, Version: v,
	}, &dr); st != 200 {
		t.Fatalf("delete: %d %s", st, body)
	}
}

func TestUpdateTextRPC(t *testing.T) {
	hs, root := newTestServer(t)
	var nr rpc.TileResponse
	if st, _ := callRPC(t, hs, "CreateText", &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root,
		X: 0, Y: 0, W: 1, H: 1, Data: []byte("v1"),
	}, &nr); st != 200 {
		t.Fatal("create text")
	}
	id := nr.Tile.ID
	v := nr.Tile.Version

	if st, body := callRPC(t, hs, "UpdateText", &rpc.UpdateTextRequest{
		Path: rpc.Path{}, TileID: id, Version: v, Data: []byte("v2"),
	}, &nr); st != 200 {
		t.Fatalf("update: %d %s", st, body)
	}

	var br rpc.GetBlobResponse
	if st, _ := callRPC(t, hs, "GetBlob", &rpc.GetBlobRequest{BlobID: nr.Tile.BlobID}, &br); st != 200 {
		t.Fatal("get blob")
	}
	if string(br.Data) != "v2" {
		t.Errorf("blob = %q, want v2", br.Data)
	}
}

func TestCloneAndMoveRPCs(t *testing.T) {
	hs, root := newTestServer(t)
	var nr rpc.TileResponse
	if st, _ := callRPC(t, hs, "CreateWell", &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	}, &nr); st != 200 {
		t.Fatal("create")
	}
	src := nr.Tile.ID
	srcV := nr.Tile.Version

	if st, body := callRPC(t, hs, "CloneTile", &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: src, Version: srcV,
		DestGridID: root, DestPath: rpc.Path{},
		X: 5, Y: 5,
	}, &nr); st != 200 {
		t.Fatalf("clone: %d %s", st, body)
	}
	clone := nr.Tile.ID
	cloneV := nr.Tile.Version

	var mr rpc.TileResponse
	if st, body := callRPC(t, hs, "MoveTile", &rpc.MoveTileRequest{
		Path: rpc.Path{}, TileID: clone, Version: cloneV,
		DestGridID: root, DestPath: rpc.Path{},
		X: 8, Y: 8,
	}, &mr); st != 200 {
		t.Fatalf("move: %d %s", st, body)
	}
	if mr.Tile.X != 8 || mr.Tile.Y != 8 {
		t.Errorf("moved to %+v", mr.Tile)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	hs, _ := newTestServer(t)
	r, _ := http.NewRequest(http.MethodGet, hs.URL+"/rpc/Bootstrap", nil)
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
	hs, root := newTestServer(t)

	// Create one tile, then create another overlapping it: ErrOverlap → 409.
	var nr rpc.TileResponse
	if st, body := callRPC(t, hs, "CreateWell", &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 2, H: 2,
	}, &nr); st != 200 {
		t.Fatalf("first create: %d %s", st, body)
	}
	st, _ := callRPC(t, hs, "CreateWell", &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 1, Y: 1, W: 1, H: 1,
	}, nil)
	if st != http.StatusConflict {
		t.Errorf("overlap: status %d, want 409", st)
	}

	// Invalid path: bogus well id → 400.
	st, _ = callRPC(t, hs, "CreateWell", &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []int64{99}},
		GridID: 1, X: 10, Y: 10, W: 1, H: 1,
	}, nil)
	if st != http.StatusBadRequest {
		t.Errorf("invalid path: status %d, want 400", st)
	}

	// Invalid argument: non-http URL → 400.
	st, _ = callRPC(t, hs, "CreateURL", &rpc.CreateURLRequest{
		Path: rpc.Path{}, GridID: root,
		X: 10, Y: 10, W: 1, H: 1, URL: "ftp://evil.example.com",
	}, nil)
	if st != http.StatusBadRequest {
		t.Errorf("bad url: status %d, want 400", st)
	}
}

func TestBootstrapIncludesRootView(t *testing.T) {
	hs, root := newTestServer(t)

	var resp rpc.BootstrapResponse
	if st, body := callRPC(t, hs, "Bootstrap", &rpc.BootstrapRequest{}, &resp); st != 200 {
		t.Fatalf("bootstrap: %d %s", st, body)
	}
	if resp.RootGridID != root {
		t.Errorf("root_grid_id = %d, want %d", resp.RootGridID, root)
	}
	// Default seed: (0, 0, 1).
	if resp.RootViewCx != 0 || resp.RootViewCy != 0 || resp.RootZoom != 1 {
		t.Errorf("initial bootstrap view = (%v,%v,%v), want (0,0,1)",
			resp.RootViewCx, resp.RootViewCy, resp.RootZoom)
	}

	if st, body := callRPC(t, hs, "SetRootView", &rpc.SetRootViewRequest{
		Cx: 11, Cy: 22, Zoom: 3,
	}, &rpc.SetRootViewResponse{}); st != 200 {
		t.Fatalf("set root view: %d %s", st, body)
	}

	if st, body := callRPC(t, hs, "Bootstrap", &rpc.BootstrapRequest{}, &resp); st != 200 {
		t.Fatalf("bootstrap 2: %d %s", st, body)
	}
	if resp.RootViewCx != 11 || resp.RootViewCy != 22 || resp.RootZoom != 3 {
		t.Errorf("after SetRootView, bootstrap = (%v,%v,%v), want (11,22,3)",
			resp.RootViewCx, resp.RootViewCy, resp.RootZoom)
	}
}

func TestVersionConflictReturns409(t *testing.T) {
	hs, root := newTestServer(t)

	var nr rpc.TileResponse
	if st, body := callRPC(t, hs, "CreateText", &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("v1"),
	}, &nr); st != 200 {
		t.Fatalf("create: %d %s", st, body)
	}
	id := nr.Tile.ID
	good := nr.Tile.Version

	// Bump version once via a successful UpdateText.
	if st, body := callRPC(t, hs, "UpdateText", &rpc.UpdateTextRequest{
		Path: rpc.Path{}, TileID: id, Version: good, Data: []byte("v2"),
	}, &nr); st != 200 {
		t.Fatalf("first update: %d %s", st, body)
	}

	// Now retry with the stale claimed version: must 409.
	st, body := callRPC(t, hs, "UpdateText", &rpc.UpdateTextRequest{
		Path: rpc.Path{}, TileID: id, Version: good, Data: []byte("v3"),
	}, nil)
	if st != http.StatusConflict {
		t.Errorf("stale version: status %d body=%s, want 409", st, body)
	}
}
