package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/internal/plugin"
	fsplugin "github.com/josephburnett/gridwell/internal/plugin/fs"
	procplugin "github.com/josephburnett/gridwell/internal/plugin/proc"
	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/store"
)

// fsPluginUUID / procPluginUUID are the registry keys used by the
// plugin-wired test server.
const (
	fsPluginUUID   = "fs-test-uuid"
	procPluginUUID = "proc-test-uuid"
)

// newTestServerWithPlugins builds a server wired to in-process fs and proc
// plugins (with the localdb UUID set), so file/process-well creation routes
// through the plugins exactly as in production. Returns the client and the
// bare local root grid id.
func newTestServerWithPlugins(t *testing.T) (*rpc.Client, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg := plugin.NewRegistry()
	_, root := registerPrimaryLocaldb(t, reg, st)

	fsP, err := fsplugin.Open(":memory:", nil)
	if err != nil {
		t.Fatalf("fs open: %v", err)
	}
	t.Cleanup(func() { _ = fsP.Close() })
	fsClient, fsCloser, err := plugin.ServeInProcess(fsP)
	if err != nil {
		t.Fatalf("fs serve: %v", err)
	}
	t.Cleanup(fsCloser)
	reg.Register(fsPluginUUID, "fs", fsClient, nil)

	procP, err := procplugin.Open(":memory:", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("proc open: %v", err)
	}
	t.Cleanup(func() { _ = procP.Close() })
	procClient, procCloser, err := plugin.ServeInProcess(procP)
	if err != nil {
		t.Fatalf("proc serve: %v", err)
	}
	t.Cleanup(procCloser)
	reg.Register(procPluginUUID, "proc", procClient, nil)

	srv := New(reg, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	return cl, root
}

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

	// Body is fetched by routable tile id (GetBlob is unroutable in the
	// rootless model — blob ids carry no plugin namespace).
	data, err := cl.GetTileContent(ctx, tile.ID)
	if err != nil {
		t.Fatalf("get tile content: %v", err)
	}
	if string(data) != "# hi" {
		t.Errorf("content = %q", data)
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

// TestCreateFileWellRPC: a file well is a plain well tile whose child grid
// lives in the fs plugin — child_grid_id is the qualified "<fs-uuid>/<id>"
// returned by the plugin's Attach.
func TestCreateFileWellRPC(t *testing.T) {
	cl, root := newTestServerWithPlugins(t)
	tile, err := cl.CreateFileWell(context.Background(), &rpc.CreateFileWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: "/etc",
	})
	if err != nil {
		t.Fatalf("create file well: %v", err)
	}
	if tile.Kind != rpc.KindWell {
		t.Errorf("kind = %q, want %q", tile.Kind, rpc.KindWell)
	}
	if !strings.HasPrefix(tile.ChildGridID, fsPluginUUID+"/") {
		t.Errorf("child_grid_id = %q, want prefix %q/", tile.ChildGridID, fsPluginUUID)
	}
	if tile.AltText != "etc" {
		t.Errorf("alt_text = %q, want %q (plugin-supplied label)", tile.AltText, "etc")
	}
}

// TestCreateProcessWellRPC: a process well is a plain well whose child grid is
// in the proc plugin.
func TestCreateProcessWellRPC(t *testing.T) {
	cl, root := newTestServerWithPlugins(t)
	tile, err := cl.CreateProcessWell(context.Background(), &rpc.CreateProcessWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, PID: 1,
	})
	if err != nil {
		t.Fatalf("create process well: %v", err)
	}
	if tile.Kind != rpc.KindWell {
		t.Errorf("kind = %q, want %q", tile.Kind, rpc.KindWell)
	}
	if !strings.HasPrefix(tile.ChildGridID, procPluginUUID+"/") {
		t.Errorf("child_grid_id = %q, want prefix %q/", tile.ChildGridID, procPluginUUID)
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

// TestSetRootViewRPC: SetRootView is a no-op in the rootless model (no app
// root; pane viewport lives in the URL). It must still succeed.
func TestSetRootViewRPC(t *testing.T) {
	_, cl, _ := newTestServer(t)
	if err := cl.SetRootView(context.Background(), &rpc.SetRootViewRequest{Cx: 3, Cy: 4, Zoom: 2}); err != nil {
		t.Fatalf("set root view: %v", err)
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
	data, err := cl.GetTileContent(ctx, tile.ID)
	if err != nil {
		t.Fatalf("get tile content: %v", err)
	}
	if string(data) != "v2" {
		t.Errorf("content = %q, want v2", data)
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

	// Invalid path (bogus well id, qualified to the primary) → InvalidArgument.
	pUUID, _, _ := splitPluginID(root)
	_, err = cl.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []string{pUUID + "/99"}},
		GridID: root, X: 10, Y: 10, W: 1, H: 1,
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

// TestBootstrapIsEmpty: in the rootless model Bootstrap returns no root grid —
// the client starts with empty panes and builds the launcher from ListPlugins.
func TestBootstrapIsEmpty(t *testing.T) {
	_, cl, _ := newTestServer(t)
	resp, err := cl.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if resp.RootGridID != "" {
		t.Errorf("root_grid_id = %q, want empty (no root)", resp.RootGridID)
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

// TestListPlugins: the launcher source lists configured plugins in config
// order, with kind, label, and writability (only localdb accepts new tiles).
func TestListPlugins(t *testing.T) {
	cl, _ := newTestServerWithPlugins(t)
	plugins, err := cl.ListPlugins(context.Background())
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	// Registration order: primary localdb, then fs, then proc.
	if len(plugins) != 3 {
		t.Fatalf("got %d plugins, want 3: %+v", len(plugins), plugins)
	}
	if plugins[0].Kind != "localdb" || !plugins[0].Writable {
		t.Errorf("plugin[0] = %+v, want writable localdb", plugins[0])
	}
	if plugins[1].Kind != "fs" || plugins[1].Writable {
		t.Errorf("plugin[1] = %+v, want read-only fs", plugins[1])
	}
	if plugins[2].Kind != "proc" || plugins[2].Writable {
		t.Errorf("plugin[2] = %+v, want read-only proc", plugins[2])
	}
}

// TestMountRPC: mounting a plugin drops an exit well in the destination grid
// whose child is the plugin's (default-config) root.
func TestMountRPC(t *testing.T) {
	cl, root := newTestServerWithPlugins(t)
	tile, err := cl.Mount(context.Background(), &rpc.MountRequest{
		PluginUUID: procPluginUUID, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if tile.Kind != rpc.KindWell {
		t.Errorf("kind = %q, want well", tile.Kind)
	}
	if !strings.HasPrefix(tile.ChildGridID, procPluginUUID+"/") {
		t.Errorf("child_grid_id = %q, want %q prefix", tile.ChildGridID, procPluginUUID)
	}
}
