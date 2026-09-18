package server

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// fsPluginUUID / procPluginUUID are the registry keys used by the
// plugin-wired test server.
const (
	fsPluginUUID   = "fs-test-uuid"
	procPluginUUID = "proc-test-uuid"
)

// newTestServerWithPlugins wires a server to the spawned fs and proc plugins,
// the fs one rooted at a fresh temp dir returned as fsRoot, so well creation
// routes through the plugins exactly as in production.
// newPluginClient stands up the plugin stack the way the loader does: the
// shipped gridwell-plugin-<kind> binary spawned with cfg, fronted by the
// pluginhost adapter over a fresh store. A plugin lives in its own repository,
// so the subprocess is the only way to reach one.
func newPluginClient(t *testing.T, kind string, cfg map[string]string) namespace.Namespace {
	t.Helper()
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatalf("%s layout: %v", kind, err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp := plugintest.Spawn(t, kind, cfg)
	return pluginhost.New(cp, memStore.Namespace("p1"), nil)
}

func registerPluginPlugin(t *testing.T, reg *plugin.Registry, uuid, kind string, cfg map[string]string) {
	t.Helper()
	reg.Register(uuid, kind, newPluginClient(t, kind, cfg), nil)
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
	registerPluginPlugin(t, reg, fsPluginUUID, "fs", map[string]string{"root": fsRoot})
	reg.SetLabel(fsPluginUUID, "files")

	// Rooted at this test process, so the projected tree is this test's own
	// couple of children rather than the whole machine's process table.
	registerPluginPlugin(t, reg, procPluginUUID, "proc", map[string]string{"pid": strconv.Itoa(os.Getpid())})
	reg.SetLabel(procPluginUUID, "processes")

	srv := mustNew(t, reg, Config{})
	hs := serveWeb(t, srv)
	cl = rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	return cl, root, fsRoot
}

func TestCreateTextRPC(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 1, Y: 1, W: 1, H: 1}}, []byte("# hi"))
	if err != nil {
		t.Fatalf("create text: %v", err)
	}
	if tile.Kind != rpc.KindText {
		t.Errorf("got kind %q, want %q", tile.Kind, rpc.KindText)
	}
	if tile.BlobId == 0 {
		t.Error("blob_id = 0, want non-zero")
	}

	// Body is fetched by routable tile id (GetBlob is unroutable in the
	// rootless model — blob ids carry no plugin namespace).
	data, _, version, err := cl.ReadContent(ctx, tile.Id)
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
	upd, err := cl.WriteContent(ctx, tile.Id, tile.Version, []byte("# hi v2"))
	if err != nil {
		t.Fatalf("update text: %v", err)
	}
	data, _, version, err = cl.ReadContent(ctx, tile.Id)
	if err != nil {
		t.Fatalf("get tile content after edit: %v", err)
	}
	if string(data) != "# hi v2" || version != upd.Version {
		t.Errorf("after edit: content = %q version = %d, want %q at version %d", data, version, "# hi v2", upd.Version)
	}
}

func TestCreateURLRPC(t *testing.T) {
	_, cl, root := newTestServer(t)
	tile, err := cl.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1, UrlString: "https://example.com"}})
	if err != nil {
		t.Fatalf("create url: %v", err)
	}
	if tile.Kind != rpc.KindURL {
		t.Errorf("got kind %q, want %q", tile.Kind, rpc.KindURL)
	}
	if tile.UrlString != "https://example.com" {
		t.Errorf("url_string = %q", tile.UrlString)
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
	if !strings.HasPrefix(tile.ChildGridId, fsPluginUUID+"/") {
		t.Errorf("child_grid_id = %q, want prefix %q/", tile.ChildGridId, fsPluginUUID)
	}
}

// mountByClone mounts a plugin into a grid the way the UI does: drag the
// plugin's + menu swatch = CreateWell with the doorway's qualified grid as
// the child (an exit-well LINK), labeled with the swatch's label
// (client/wasm createPluginLinkAtCell). Which swatches a row contributes is
// door.PlacesOf, the client's own rule, so this drags what the user drags.
// Every plugin under test here declares one collection, so there is one.
func mountByClone(t *testing.T, cl *rpc.Client, pluginUUID, destGrid string, x, y int64) *gridwellv1.Tile {
	t.Helper()
	ctx := context.Background()
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	var places []door.Place
	for _, p := range lp.Plugins {
		if p.Uuid == pluginUUID {
			places = door.PlacesOf(p)
		}
	}
	if len(places) != 1 {
		t.Fatalf("mount %s: %d swatches in %+v, want one", pluginUUID, len(places), lp.Plugins)
	}
	row := places[0].Plugin
	tile, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: destGrid, Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: x, Y: y, W: 1, H: 1, ChildGridId: row.RootGridId, AltText: row.Label, ViewCx: row.RootViewCx, ViewCy: row.RootViewCy, ViewZoom: row.RootViewZoom}})
	if err != nil {
		t.Fatalf("mount %s by link: %v", pluginUUID, err)
	}
	return tile
}

// TestMenuAndMountLabelAgree: the label the menu shows for a plugin
// (Handshake) and the label carried onto a mounted link are the same
// server.yaml display name — never a plugin-derived string.
func TestMenuAndMountLabelAgree(t *testing.T) {
	cl, root, _ := newTestServerWithPlugins(t)
	ctx := context.Background()

	plugins, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	var menuLabel string
	for _, p := range plugins.Plugins {
		if p.Uuid == fsPluginUUID {
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

func TestResizeAndSetFramingRPCs(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := tile.Id

	resized, err := cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: id, GridId: root, X: 0, Y: 0, W: 2, H: 2,
	})
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if resized.W != 2 || resized.H != 2 {
		t.Errorf("after resize: %+v", resized)
	}

	tile, err = cl.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		TileId: id, Cx: 7, Cy: 8, Zoom: 1.5,
	})
	if err != nil {
		t.Fatalf("set well view: %v", err)
	}
	if tile.ViewCx != 7 || tile.ViewCy != 8 || tile.ViewZoom != 1.5 {
		t.Errorf("after set well view: %+v", tile)
	}
}

func TestSetTextViewRPC(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("hi"))
	if err != nil {
		t.Fatalf("create text: %v", err)
	}
	id := tile.Id

	tile, err = cl.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: id, Tile: &gridwellv1.Tile{Kind: rpc.KindText, TextX: 1, TextY: 2, TextW: 3, TextH: 4, TextMode: rpc.TextModeRendered}})
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
	tile, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: tile.Id}); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestUpdateTextRPC(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()
	tile, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("v1"))
	if err != nil {
		t.Fatalf("create text: %v", err)
	}
	tile, err = cl.WriteContent(ctx, tile.Id, tile.Version, []byte("v2"))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	data, _, _, err := cl.ReadContent(ctx, tile.Id)
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
	tile, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	clone, err := cl.CloneTile(ctx, &gridwellv1.CloneTileRequest{
		TileId:     tile.Id,
		DestGridId: root, X: 5, Y: 5,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	moved, err := cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: clone.Id,
		GridId: root, X: 8, Y: 8, W: clone.W, H: clone.H,
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

	if _, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 0, Y: 0, W: 2, H: 2}}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Overlap → FailedPrecondition.
	_, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 1, Y: 1, W: 1, H: 1}})
	if got := errCode(err); got != connect.CodeFailedPrecondition {
		t.Errorf("overlap: code %v, want FailedPrecondition", got)
	}

	// A create into a grid that doesn't exist → InvalidArgument (grid_id is
	// the authoritative location; there is no descent path).
	pUUID, _, _ := rpc.SplitID(root)
	_, err = cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: pUUID + "/999999", Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 10, Y: 10, W: 1, H: 1}})
	if got := errCode(err); got != connect.CodeInvalidArgument {
		t.Errorf("missing grid: code %v, want InvalidArgument", got)
	}

	// Non-http URL → InvalidArgument.
	_, err = cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 10, Y: 10, W: 1, H: 1, UrlString: "ftp://evil.example.com"}})
	if got := errCode(err); got != connect.CodeInvalidArgument {
		t.Errorf("bad url: code %v, want InvalidArgument", got)
	}
}

func TestVersionConflictReturnsFailedPrecondition(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("v1"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	good := tile.Version

	// Bump version via a successful UpdateText.
	if _, err := cl.WriteContent(ctx, tile.Id, good, []byte("v2")); err != nil {
		t.Fatalf("first update: %v", err)
	}
	// Retry with stale claimed version.
	_, err = cl.WriteContent(ctx, tile.Id, good, []byte("v3"))
	if got := errCode(err); got != connect.CodeFailedPrecondition {
		t.Errorf("stale version: code %v, want FailedPrecondition", got)
	}
}

// TestListPlugins: the + menu source lists configured plugins in config
// order, with kind and label.
func TestListPlugins(t *testing.T) {
	cl, _, _ := newTestServerWithPlugins(t)
	list, err := cl.Handshake(context.Background())
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	plugins := list.Plugins
	// Registration order: home, then fs, then proc.
	if len(plugins) != 3 {
		t.Fatalf("got %d plugins, want 3: %+v", len(plugins), plugins)
	}
	if plugins[0].Kind != "home" {
		t.Errorf("plugin[0] = %+v, want the home row", plugins[0])
	}
	// Each plugin advertises its qualified root grid id (for click-enter).
	if !strings.HasPrefix(plugins[0].RootGridId, plugins[0].Uuid+"/") {
		t.Errorf("plugin[0] root_grid_id = %q, want %q prefix", plugins[0].RootGridId, plugins[0].Uuid)
	}
	// Home advertises a qualified scratch grid id (the ephemeral-url
	// target), distinct from its root. fs/proc have none.
	if !strings.HasPrefix(plugins[0].ScratchGridId, plugins[0].Uuid+"/") {
		t.Errorf("plugin[0] scratch_grid_id = %q, want %q prefix", plugins[0].ScratchGridId, plugins[0].Uuid)
	}
	if plugins[0].ScratchGridId == plugins[0].RootGridId {
		t.Errorf("scratch grid id %q must differ from root", plugins[0].ScratchGridId)
	}
	if plugins[1].ScratchGridId != "" {
		t.Errorf("fs plugin should have no scratch grid, got %q", plugins[1].ScratchGridId)
	}
	if plugins[1].Kind != "fs" {
		t.Errorf("plugin[1] = %+v, want fs", plugins[1])
	}
	if plugins[2].Kind != "proc" {
		t.Errorf("plugin[2] = %+v, want proc", plugins[2])
	}
}

// TestCreateScratchURLRoutes: creating a url tile whose grid is home's
// qualified scratch grid is an ephemeral visit — it routes path-free into the
// scratch grid (no descent path leads there) and the tile carries reference=false
// (it's owned content, just off-grid), with the typed URL. This proves the
// whole client→server→plugin path-free path the "descend into a url" feature uses.
func TestCreateScratchURLRoutes(t *testing.T) {
	cl, _, _ := newTestServerWithPlugins(t)
	ctx := context.Background()
	plugins, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	scratch := plugins.Plugins[0].ScratchGridId
	if scratch == "" {
		t.Fatal("localdb advertised no scratch grid")
	}
	// Empty path + scratch grid: a normal create here would fail path validation
	// (the scratch grid is off-grid); the scratch route bypasses it.
	tile, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: scratch, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1, UrlString: "https://example.com/ephemeral"}})
	if err != nil {
		t.Fatalf("create ephemeral url into scratch: %v", err)
	}
	if tile.Kind != rpc.KindURL || tile.UrlString != "https://example.com/ephemeral" {
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
	if len(g.Tiles) != 1 || g.Tiles[0].Id != tile.Id {
		t.Errorf("scratch grid tiles = %+v, want the one ephemeral url", g.Tiles)
	}
}

// TestPluginGridCarriesHomeScratch: a grid served by a plugin that declares
// no scratch grid of its own — fs, proc, gitlab — carries the node's home
// scratch grid instead. The grid is the carrier because it chains through
// mounts; the stamp is where the client learns that a link clicked in a
// rendered document inside a plugin grid has somewhere to land as an
// ephemeral visit. Without it the visit could not open.
func TestPluginGridCarriesHomeScratch(t *testing.T) {
	cl, _, _ := newTestServerWithPlugins(t)
	ctx := context.Background()
	hs, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	homeScratch := hs.Plugins[0].ScratchGridId
	if homeScratch == "" {
		t.Fatal("home advertised no scratch grid")
	}
	fsRoot := plugintest.LandingOf(t, hs.Plugins[1])
	g, err := cl.GetGrid(ctx, fsRoot)
	if err != nil {
		t.Fatalf("GetGrid fs root: %v", err)
	}
	if g.Grid.ScratchGridId != homeScratch {
		t.Fatalf("fs grid scratch_grid_id = %q, want home's %q", g.Grid.ScratchGridId, homeScratch)
	}
	// The stamp must not be a dangling pointer: a visit created against it
	// routes into home, exactly what a link click does.
	tile, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: g.Grid.ScratchGridId, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1, UrlString: "https://gitlab.example/g/p/-/merge_requests/1"}})
	if err != nil {
		t.Fatalf("create ephemeral url into stamped scratch: %v", err)
	}
	if tile.Kind != rpc.KindURL {
		t.Errorf("kind = %q, want url", tile.Kind)
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
	if !strings.HasPrefix(tile.ChildGridId, procPluginUUID+"/") {
		t.Errorf("child_grid_id = %q, want %q prefix", tile.ChildGridId, procPluginUUID)
	}
	// A mount is a LINK: the server must stamp reference=true on the way back
	// through the wire, so the client renders it dashed (and a delete unlinks
	// only). This is the bit render reads instead of guessing from uuids.
	if !tile.Reference {
		t.Error("mounted well must arrive as a reference (dashed link), got reference=false")
	}
}

// TestFramingRoundTripsByteIdenticalAcrossTheSeam is the S4 seam pin:
// store → wire → client, both rows that can own framing, byte for byte.
// A unit test on either side would not catch it — the bug this shape
// replaces was exactly a representation mismatch between the two (an
// integer window origin in the store, a float center in the client), and
// the whole quantization apparatus existed to survive it. Awkward values
// on purpose: sub-cell centers, a negative, and a zoom with no exact
// binary form.
func TestFramingRoundTripsByteIdenticalAcrossTheSeam(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	want := rpc.Framing{Cx: 5.37, Cy: -7.125, Zoom: 0.1}

	// The doorway row: a well tile.
	well, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 3, Y: 3, W: 3, H: 5}})
	if err != nil {
		t.Fatal(err)
	}
	set, err := cl.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		TileId: well.Id, Cx: want.Cx, Cy: want.Cy, Zoom: want.Zoom,
	})
	if err != nil {
		t.Fatalf("SetFraming(doorway): %v", err)
	}
	if got := (rpc.Framing{Cx: set.ViewCx, Cy: set.ViewCy, Zoom: set.ViewZoom}); got != want {
		t.Errorf("the write's own response = %+v, want %+v", got, want)
	}
	// Read it back the way the client actually reads a doorway: through
	// the grid it lives in.
	g, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tile := range g.Tiles {
		if tile.Id != well.Id {
			continue
		}
		found = true
		if got := (rpc.Framing{Cx: tile.ViewCx, Cy: tile.ViewCy, Zoom: tile.ViewZoom}); got != want {
			t.Errorf("doorway framing read back = %+v, want %+v", got, want)
		}
	}
	if !found {
		t.Fatal("the well is not in the grid it was created in")
	}

	// The root row: the same verb, the same shape, no doorway. It reads
	// back through the handshake, where a root's framing rides.
	if _, err := cl.SetFraming(ctx, &gridwellv1.SetFramingRequest{RootGridId: root, Cx: want.Cx, Cy: want.Cy, Zoom: want.Zoom}); err != nil {
		t.Fatalf("SetFraming(root): %v", err)
	}
	hs, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootRow := false
	for _, pl := range hs.Plugins {
		if pl.RootGridId != root {
			continue
		}
		rootRow = true
		if got := (rpc.Framing{Cx: pl.RootViewCx, Cy: pl.RootViewCy, Zoom: pl.RootViewZoom}); got != want {
			t.Errorf("root framing read back = %+v, want %+v", got, want)
		}
	}
	if !rootRow {
		t.Fatalf("no handshake row for the root grid %s", root)
	}

	// And the guiding rule: re-writing the SAME framing changes nothing.
	if _, err := cl.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		TileId: well.Id, Cx: want.Cx, Cy: want.Cy, Zoom: want.Zoom,
	}); err != nil {
		t.Fatalf("SetFraming(doorway, again): %v", err)
	}
	again, err := cl.GetTile(ctx, well.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := (rpc.Framing{Cx: again.ViewCx, Cy: again.ViewCy, Zoom: again.ViewZoom}); got != want {
		t.Errorf("rewriting the same framing moved it: %+v, want %+v", got, want)
	}
	if again.Version != set.Version {
		t.Errorf("a framing write bumped version %d → %d", set.Version, again.Version)
	}
}
