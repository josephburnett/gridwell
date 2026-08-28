package server

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/compose"
	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/layout"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	fsplugin "github.com/josephburnett/gridwell/plugins/fs/plugin"
	procplugin "github.com/josephburnett/gridwell/plugins/proc/plugin"
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
// newTestServerWithPlugins stands up a rootless server with the primary
// localdb plus built-in fs and proc plugins. The fs plugin is rooted at a
// fresh temp dir (returned as fsRoot) so a Mount of fsPluginUUID — which
// attaches with the plugin's default config — lands there.
// newPluginClient stands up the v2 plugin stack the way the loader
// does in production — the plugin served in-process, fronted by the
// node-side pluginhost adapter over a fresh layout memory DB — and
// returns the adapter's client. The SHIPPED fs/proc stack: server tests
// must exercise it, not a stand-in.
func newPluginClient(t *testing.T, kind string, impl pluginv1.PluginServer) pb.GridwellClient {
	t.Helper()
	mem, err := layout.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatalf("%s layout: %v", kind, err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	cp, cpCloser, err := compose.PluginInProcess(impl)
	if err != nil {
		t.Fatalf("%s plugin serve: %v", kind, err)
	}
	t.Cleanup(cpCloser)
	client, closer, err := plugin.ServeInProcess(pluginhost.New(cp, mem))
	if err != nil {
		t.Fatalf("%s adapter serve: %v", kind, err)
	}
	t.Cleanup(closer)
	return client
}

func registerPluginPlugin(t *testing.T, reg *plugin.Registry, uuid, kind string, impl pluginv1.PluginServer) {
	t.Helper()
	reg.Register(uuid, kind, newPluginClient(t, kind, impl), nil)
}

func newTestServerWithPlugins(t *testing.T) (cl *rpc.Client, root, fsRoot string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg := plugin.NewRegistry()
	_, root = registerPrimaryLocaldb(t, reg, st)

	fsRoot = t.TempDir()
	registerPluginPlugin(t, reg, fsPluginUUID, "fs", fsplugin.New(fsRoot, nil))
	reg.SetLabel(fsPluginUUID, "files")

	registerPluginPlugin(t, reg, procPluginUUID, "proc", procplugin.New(t.TempDir(), 1, nil))
	reg.SetLabel(procPluginUUID, "processes")

	srv := mustNew(t, reg, Config{NodeID: "tnode"})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	cl = rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	return cl, root, fsRoot
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
	data, _, version, err := cl.ReadContent(ctx, tile.ID)
	if err != nil {
		t.Fatalf("get tile content: %v", err)
	}
	if string(data) != "# hi" {
		t.Errorf("content = %q", data)
	}
	if version != tile.Version {
		t.Errorf("content version = %d, want the tile row's %d", version, tile.Version)
	}

	// The bytes↔version pairing is the client's save basis: after an edit
	// bumps the row, a re-fetch must return the NEW version with the new
	// bytes — pairing them in one plugin read is what lets a client never
	// claim a version whose content it hasn't seen.
	upd, err := cl.WriteContent(ctx, tile.ID, tile.Version, []byte("# hi v2"))
	if err != nil {
		t.Fatalf("update text: %v", err)
	}
	data, _, version, err = cl.ReadContent(ctx, tile.ID)
	if err != nil {
		t.Fatalf("get tile content after edit: %v", err)
	}
	if string(data) != "# hi v2" || version != upd.Version {
		t.Errorf("after edit: content = %q version = %d, want %q at version %d", data, version, "# hi v2", upd.Version)
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

// TestMountFsPlugin: mounting the fs plugin drops a plain well tile whose
// child grid lives in the fs plugin — child_grid_id is the qualified
// "<fs-uuid>/<id>". (TestMountByClone covers the proc plugin; this covers fs,
// the other built-in source.)
func TestMountFsPlugin(t *testing.T) {
	cl, root, _ := newTestServerWithPlugins(t)
	tile := mountByClone(t, cl, fsPluginUUID, root, 0, 0)
	if tile.Kind != rpc.KindWell {
		t.Errorf("kind = %q, want %q", tile.Kind, rpc.KindWell)
	}
	if !strings.HasPrefix(tile.ChildGridID, fsPluginUUID+"/") {
		t.Errorf("child_grid_id = %q, want prefix %q/", tile.ChildGridID, fsPluginUUID)
	}
}

// mountByClone mounts a plugin into a grid the way the UI does: right-drag =
// CloneTile of the plugin's NODE-GRID link tile (whose local tile id is the
// plugin uuid). The Mount RPC this replaced had no callers.
func mountByClone(t *testing.T, cl *rpc.Client, pluginUUID, destGrid string, x, y int64) *rpc.Tile {
	t.Helper()
	tile, err := cl.CloneTile(context.Background(), &rpc.CloneTileRequest{
		TileID: "tnode/" + pluginUUID, Version: 0,
		DestGridID: destGrid, X: x, Y: y,
	})
	if err != nil {
		t.Fatalf("mount %s by clone: %v", pluginUUID, err)
	}
	return tile
}

// TestMenuAndMountLabelAgree: the label the launcher shows for a plugin
// (ListPlugins / the node-grid tile) and the label carried onto a mounted
// link (clone of that tile) are the same server.yaml display name — never a
// plugin-derived string. One owner: the node-grid tile's alt.
func TestMenuAndMountLabelAgree(t *testing.T) {
	cl, root, _ := newTestServerWithPlugins(t)
	ctx := context.Background()

	plugins, err := cl.ListPlugins(ctx)
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	var menuLabel string
	for _, p := range plugins.Plugins {
		if p.UUID == fsPluginUUID {
			menuLabel = p.Label
		}
	}
	if menuLabel != "files" {
		t.Fatalf("menu label = %q, want the configured %q", menuLabel, "files")
	}

	tile := mountByClone(t, cl, fsPluginUUID, root, 0, 0)
	if tile.AltText != menuLabel {
		t.Errorf("dropped well label = %q, want %q (must match the menu)", tile.AltText, menuLabel)
	}
	_ = ctx
}

func TestResizeAndSetWellViewRPCs(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id, v := tile.ID, tile.Version

	tile, err = cl.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: id, Version: v, GridID: root, X: 0, Y: 0, W: 2, H: 2,
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
	tile, err = cl.WriteContent(ctx, tile.ID, tile.Version, []byte("v2"))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	data, _, _, err := cl.ReadContent(ctx, tile.ID)
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
	moved, err := cl.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: clone.ID, Version: clone.Version,
		GridID: root, X: 8, Y: 8, W: clone.W, H: clone.H,
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

	// A create into a grid that doesn't exist → InvalidArgument (grid_id is
	// the authoritative location; there is no descent path).
	pUUID, _, _ := rpc.SplitID(root)
	_, err = cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: pUUID + "/999999", X: 10, Y: 10, W: 1, H: 1,
	})
	if got := errCode(err); got != connect.CodeInvalidArgument {
		t.Errorf("missing grid: code %v, want InvalidArgument", got)
	}

	// Non-http URL → InvalidArgument.
	_, err = cl.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: root, X: 10, Y: 10, W: 1, H: 1, URL: "ftp://evil.example.com",
	})
	if got := errCode(err); got != connect.CodeInvalidArgument {
		t.Errorf("bad url: code %v, want InvalidArgument", got)
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
	if _, err := cl.WriteContent(ctx, tile.ID, good, []byte("v2")); err != nil {
		t.Fatalf("first update: %v", err)
	}
	// Retry with stale claimed version.
	_, err = cl.WriteContent(ctx, tile.ID, good, []byte("v3"))
	if got := errCode(err); got != connect.CodeFailedPrecondition {
		t.Errorf("stale version: code %v, want FailedPrecondition", got)
	}
}

// TestListPlugins: the launcher source lists configured plugins in config
// order, with kind, label, and writability (only localdb accepts new tiles).
func TestListPlugins(t *testing.T) {
	cl, _, _ := newTestServerWithPlugins(t)
	list, err := cl.ListPlugins(context.Background())
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	plugins := list.Plugins
	// Registration order: primary localdb, then fs, then proc.
	if len(plugins) != 3 {
		t.Fatalf("got %d plugins, want 3: %+v", len(plugins), plugins)
	}
	if plugins[0].Kind != "home" || !plugins[0].Writable {
		t.Errorf("plugin[0] = %+v, want writable localdb", plugins[0])
	}
	// Each plugin advertises its qualified root grid id (for click-enter).
	if !strings.HasPrefix(plugins[0].RootGridID, plugins[0].UUID+"/") {
		t.Errorf("plugin[0] root_grid_id = %q, want %q prefix", plugins[0].RootGridID, plugins[0].UUID)
	}
	// The localdb advertises a qualified scratch grid id (the ephemeral-url
	// target), distinct from its root. fs/proc have none.
	if !strings.HasPrefix(plugins[0].ScratchGridID, plugins[0].UUID+"/") {
		t.Errorf("plugin[0] scratch_grid_id = %q, want %q prefix", plugins[0].ScratchGridID, plugins[0].UUID)
	}
	if plugins[0].ScratchGridID == plugins[0].RootGridID {
		t.Errorf("scratch grid id %q must differ from root", plugins[0].ScratchGridID)
	}
	if plugins[1].ScratchGridID != "" {
		t.Errorf("fs plugin should have no scratch grid, got %q", plugins[1].ScratchGridID)
	}
	if plugins[1].Kind != "fs" || plugins[1].Writable {
		t.Errorf("plugin[1] = %+v, want read-only fs", plugins[1])
	}
	if plugins[2].Kind != "proc" || plugins[2].Writable {
		t.Errorf("plugin[2] = %+v, want read-only proc", plugins[2])
	}
}

// TestCreateScratchURLRoutes: creating a url tile whose grid is the localdb's
// qualified scratch grid is an ephemeral visit — it routes path-free into the
// scratch grid (no descent path leads there) and the tile carries reference=false
// (it's owned content, just off-grid), with the typed URL. This proves the
// whole client→server→plugin path-free path the "descend into a url" feature uses.
func TestCreateScratchURLRoutes(t *testing.T) {
	cl, _, _ := newTestServerWithPlugins(t)
	ctx := context.Background()
	plugins, err := cl.ListPlugins(ctx)
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	scratch := plugins.Plugins[0].ScratchGridID
	if scratch == "" {
		t.Fatal("localdb advertised no scratch grid")
	}
	// Empty path + scratch grid: a normal create here would fail path validation
	// (the scratch grid is off-grid); the scratch route bypasses it.
	tile, err := cl.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: scratch, X: 0, Y: 0, W: 1, H: 1,
		URL: "https://example.com/ephemeral",
	})
	if err != nil {
		t.Fatalf("create ephemeral url into scratch: %v", err)
	}
	if tile.Kind != rpc.KindURL || tile.URLString != "https://example.com/ephemeral" {
		t.Errorf("scratch tile = %+v, want a url tile with the typed URL", tile)
	}
	if tile.Reference {
		t.Error("an ephemeral url tile is owned content (off-grid), not a reference")
	}
	// It must be readable back from the scratch grid (descent + autocomplete).
	g, err := cl.GetGrid(ctx, scratch)
	if err != nil {
		t.Fatalf("GetGrid scratch: %v", err)
	}
	if len(g.Tiles) != 1 || g.Tiles[0].ID != tile.ID {
		t.Errorf("scratch grid tiles = %+v, want the one ephemeral url", g.Tiles)
	}
}

// TestMountByClone: mounting a plugin (cloning its node-grid tile) drops an
// exit well in the destination grid whose child is the plugin's root.
func TestMountByClone(t *testing.T) {
	cl, root, _ := newTestServerWithPlugins(t)
	tile := mountByClone(t, cl, procPluginUUID, root, 0, 0)
	if tile.Kind != rpc.KindWell {
		t.Errorf("kind = %q, want well", tile.Kind)
	}
	if !strings.HasPrefix(tile.ChildGridID, procPluginUUID+"/") {
		t.Errorf("child_grid_id = %q, want %q prefix", tile.ChildGridID, procPluginUUID)
	}
	// A mount is a LINK: the server must stamp reference=true on the way back
	// through the wire, so the client renders it dashed (and a delete unlinks
	// only). This is the bit render reads instead of guessing from uuids.
	if !tile.Reference {
		t.Error("mounted well must arrive as a reference (dashed link), got reference=false")
	}
}
