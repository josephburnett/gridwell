package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/internal/rpc"
)

func TestCreateTextRPC(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root, X: 1, Y: 1, W: 1, H: 1, Data: []byte("# hi"),
	})
	if err != nil {
		t.Fatalf("create text: %v", err)
	}
	if tile.Kind != rpc.KindText {
		t.Errorf("got kind %q, want %q", tile.Kind, rpc.KindText)
	}
	if tile.BlobID == 0 {
		t.Error("blob_id = 0, want non-zero")
	}

	data, err := cl.GetBlob(ctx, tile.BlobID)
	if err != nil {
		t.Fatalf("get blob: %v", err)
	}
	if string(data) != "# hi" {
		t.Errorf("blob data = %q", data)
	}
}

func TestCreateURLRPC(t *testing.T) {
	_, cl, root := newTestServer(t)
	tile, err := cl.CreateURL(context.Background(), &rpc.CreateURLRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, URL: "https://example.com",
	})
	if err != nil {
		t.Fatalf("create url: %v", err)
	}
	if tile.Kind != rpc.KindURL {
		t.Errorf("got kind %q, want %q", tile.Kind, rpc.KindURL)
	}
	if tile.URLString != "https://example.com" {
		t.Errorf("url_string = %q", tile.URLString)
	}
}

func TestCreateFileWellRPC(t *testing.T) {
	_, cl, root := newTestServer(t)
	tile, err := cl.CreateFileWell(context.Background(), &rpc.CreateFileWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: "/etc",
	})
	if err != nil {
		t.Fatalf("create file well: %v", err)
	}
	if tile.Kind != rpc.KindFileWell {
		t.Errorf("kind = %q, want %q", tile.Kind, rpc.KindFileWell)
	}
	if tile.FSPath != "/etc" {
		t.Errorf("fs_path = %q", tile.FSPath)
	}
}

func TestCreateProcessWellRPC(t *testing.T) {
	_, cl, root := newTestServer(t)
	tile, err := cl.CreateProcessWell(context.Background(), &rpc.CreateProcessWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, PID: 1,
	})
	if err != nil {
		t.Fatalf("create process well: %v", err)
	}
	if tile.Kind != rpc.KindProcessWell {
		t.Errorf("kind = %q, want %q", tile.Kind, rpc.KindProcessWell)
	}
	if tile.PID != 1 {
		t.Errorf("pid = %d, want 1", tile.PID)
	}
}

func TestResizeAndSetWellViewRPCs(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id, v := tile.ID, tile.Version

	tile, err = cl.ResizeTile(ctx, &rpc.ResizeTileRequest{
		TileID: id, Version: v, X: 0, Y: 0, W: 2, H: 2,
	})
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	v = tile.Version

	tile, err = cl.SetWellView(ctx, &rpc.SetWellViewRequest{
		TileID: id, Version: v, ViewX: 7, ViewY: 8, ViewZoom: 1.5,
	})
	if err != nil {
		t.Fatalf("set well view: %v", err)
	}
	if tile.ViewX != 7 || tile.ViewY != 8 || tile.ViewZoom != 1.5 {
		t.Errorf("after set well view: %+v", tile)
	}
}

func TestSetTextViewRPC(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("hi"),
	})
	if err != nil {
		t.Fatalf("create text: %v", err)
	}
	id, v := tile.ID, tile.Version

	tile, err = cl.SetTextView(ctx, &rpc.SetTextViewRequest{
		TileID: id, Version: v,
		TextX: 1, TextY: 2, TextW: 3, TextH: 4, TextMode: rpc.TextModeRendered,
	})
	if err != nil {
		t.Fatalf("set text view: %v", err)
	}
	if tile.TextX != 1 || tile.TextY != 2 || tile.TextW != 3 || tile.TextH != 4 {
		t.Errorf("after set text view: %+v", tile)
	}
	if tile.TextMode != rpc.TextModeRendered {
		t.Errorf("text_mode = %q, want %q", tile.TextMode, rpc.TextModeRendered)
	}
}

func TestSetRootViewRPC(t *testing.T) {
	_, cl, _ := newTestServer(t)
	ctx := context.Background()

	if err := cl.SetRootView(ctx, &rpc.SetRootViewRequest{Cx: 3, Cy: 4, Zoom: 2}); err != nil {
		t.Fatalf("set root view: %v", err)
	}
	br, err := cl.Bootstrap(ctx)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if br.RootViewCx != 3 || br.RootViewCy != 4 || br.RootZoom != 2 {
		t.Errorf("after SetRootView, bootstrap = %+v", br)
	}
}

func TestDeleteTileRPC(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()
	tile, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := cl.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: tile.ID, Version: tile.Version}); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestUpdateTextRPC(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()
	tile, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("v1"),
	})
	if err != nil {
		t.Fatalf("create text: %v", err)
	}
	tile, err = cl.UpdateText(ctx, &rpc.UpdateTextRequest{
		TileID: tile.ID, Version: tile.Version, Data: []byte("v2"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	data, err := cl.GetBlob(ctx, tile.BlobID)
	if err != nil {
		t.Fatalf("get blob: %v", err)
	}
	if string(data) != "v2" {
		t.Errorf("blob = %q, want v2", data)
	}
}

func TestCloneAndMoveRPCs(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()
	tile, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	clone, err := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: tile.ID, Version: tile.Version,
		DestGridID: root, X: 5, Y: 5,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	moved, err := cl.MoveTile(ctx, &rpc.MoveTileRequest{
		TileID: clone.ID, Version: clone.Version,
		DestGridID: root, X: 8, Y: 8,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if moved.X != 8 || moved.Y != 8 {
		t.Errorf("moved to %+v", moved)
	}
}

// TestErrorCodeMapping confirms store errors surface as the right
// Connect error codes — the wire equivalent of the old HTTP-status
// mapping.
func TestErrorCodeMapping(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	if _, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 2, H: 2,
	}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Overlap → FailedPrecondition.
	_, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 1, Y: 1, W: 1, H: 1,
	})
	if got := errCode(err); got != connect.CodeFailedPrecondition {
		t.Errorf("overlap: code %v, want FailedPrecondition", got)
	}

	// Invalid path (bogus well id) → InvalidArgument.
	_, err = cl.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []string{"99"}},
		GridID: "1", X: 10, Y: 10, W: 1, H: 1,
	})
	if got := errCode(err); got != connect.CodeInvalidArgument {
		t.Errorf("invalid path: code %v, want InvalidArgument", got)
	}

	// Non-http URL → InvalidArgument.
	_, err = cl.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: root, X: 10, Y: 10, W: 1, H: 1, URL: "ftp://evil.example.com",
	})
	if got := errCode(err); got != connect.CodeInvalidArgument {
		t.Errorf("bad url: code %v, want InvalidArgument", got)
	}
}

func TestBootstrapIncludesRootView(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	resp, err := cl.Bootstrap(ctx)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if resp.RootGridID != root {
		t.Errorf("root_grid_id = %q, want %q", resp.RootGridID, root)
	}
	if resp.RootViewCx != 0 || resp.RootViewCy != 0 || resp.RootZoom != 1 {
		t.Errorf("initial bootstrap view = (%v,%v,%v), want (0,0,1)",
			resp.RootViewCx, resp.RootViewCy, resp.RootZoom)
	}

	if err := cl.SetRootView(ctx, &rpc.SetRootViewRequest{Cx: 11, Cy: 22, Zoom: 3}); err != nil {
		t.Fatalf("set root view: %v", err)
	}
	resp, err = cl.Bootstrap(ctx)
	if err != nil {
		t.Fatalf("bootstrap 2: %v", err)
	}
	if resp.RootViewCx != 11 || resp.RootViewCy != 22 || resp.RootZoom != 3 {
		t.Errorf("after SetRootView, bootstrap = (%v,%v,%v), want (11,22,3)",
			resp.RootViewCx, resp.RootViewCy, resp.RootZoom)
	}
}

func TestVersionConflictReturnsFailedPrecondition(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("v1"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	good := tile.Version

	// Bump version via a successful UpdateText.
	if _, err := cl.UpdateText(ctx, &rpc.UpdateTextRequest{
		TileID: tile.ID, Version: good, Data: []byte("v2"),
	}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	// Retry with stale claimed version.
	_, err = cl.UpdateText(ctx, &rpc.UpdateTextRequest{
		TileID: tile.ID, Version: good, Data: []byte("v3"),
	})
	if got := errCode(err); got != connect.CodeFailedPrecondition {
		t.Errorf("stale version: code %v, want FailedPrecondition", got)
	}
}
