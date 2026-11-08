package server

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/internal/claimtest"
)

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
	// rootless model — blob ids carry no namespace).
	data, _, version, err := cl.ReadContent(ctx, tile.Id)
	if err != nil {
		t.Fatalf("get tile content: %v", err)
	}
	if string(data) != "# hi" {
		t.Errorf("content = %q", data)
	}
	if version.Version != tile.Version {
		t.Errorf("content version = %d, want the tile row's %d", version.Version, tile.Version)
	}

	// The bytes↔version pairing is the client's save basis: after an edit
	// bumps the row, a re-fetch must return the NEW version with the new
	// bytes — pairing them in one namespace read is what lets a client never
	// claim a version whose content it hasn't seen.
	upd, err := cl.WriteContent(ctx, tile.Id, rpc.ContentBasis{Version: tile.Version}, []byte("# hi v2"))
	if err != nil {
		t.Fatalf("update text: %v", err)
	}
	data, _, version, err = cl.ReadContent(ctx, tile.Id)
	if err != nil {
		t.Fatalf("get tile content after edit: %v", err)
	}
	if string(data) != "# hi v2" || version.Version != upd.Version {
		t.Errorf("after edit: content = %q version = %d, want %q at version %d", data, version.Version, "# hi v2", upd.Version)
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

// TestLinkFilesBySwatch: dragging a root's + menu swatch drops a door to
// another namespace whose child grid is the root's, in the files, and which
// arrives as a link: the server stamps link=true on the way back, so the
// client renders it dashed and a delete only unlinks.
func TestLinkFilesBySwatch(t *testing.T) {
	f := newFilesServer(t)
	tile := linkBySwatch(t, f.cl, rpc.FilesOwner(f.node), f.home, 0, 0)
	if tile.Kind != rpc.KindDoor {
		t.Errorf("kind = %q, want %q", tile.Kind, rpc.KindDoor)
	}
	if tile.ChildGridId != f.grid {
		t.Errorf("child_grid_id = %q, want the root's grid %q", tile.ChildGridId, f.grid)
	}
	if !tile.Link {
		t.Error("door must arrive as a link (dashed link), got link=false")
	}
}

// linkBySwatch links a menu row's one door into a grid the way the UI does:
// drag its + menu swatch = CreateDoor with the door's qualified grid as the
// child (a door to another namespace LINK), labeled with the swatch's label
// (client/wasm createPluginLinkAtCell). Which swatches a row contributes is
// door.PlacesOf, the client's own rule, so this drags what the user drags.
func linkBySwatch(t *testing.T, cl *rpc.Client, rowUUID, destGrid string, x, y int64) *gridwellv1.Tile {
	t.Helper()
	ctx := context.Background()
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	var places []door.Place
	for _, p := range lp.Plugins {
		if p.Uuid == rowUUID {
			places = door.PlacesOf(p)
		}
	}
	if len(places) != 1 {
		t.Fatalf("link %s: %d swatches in %+v, want one", rowUUID, len(places), lp.Plugins)
	}
	row := places[0].Plugin
	tile, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: destGrid, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: x, Y: y, W: 1, H: 1, ChildGridId: row.RootGridId, AltText: row.Label, ViewCx: row.RootViewCx, ViewCy: row.RootViewCy, ViewZoom: row.RootViewZoom}})
	if err != nil {
		t.Fatalf("link %s: %v", rowUUID, err)
	}
	return tile
}

// TestMenuAndDoorLabelAgree: the label the menu shows for a root's swatch
// and the label carried onto the door dropped from it are one string.
func TestMenuAndDoorLabelAgree(t *testing.T) {
	f := newFilesServer(t)
	lp, err := f.cl.Handshake(context.Background())
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	var menuLabel string
	for _, p := range lp.Plugins {
		if p.Uuid == rpc.FilesOwner(f.node) {
			menuLabel = door.PlacesOf(p)[0].Plugin.Label
		}
	}
	if menuLabel == "" {
		t.Fatalf("the files' swatch has no label: %+v", lp.Plugins)
	}
	tile := linkBySwatch(t, f.cl, rpc.FilesOwner(f.node), f.home, 0, 0)
	if tile.AltText != menuLabel {
		t.Errorf("dropped door label = %q, want %q (must match the menu)", tile.AltText, menuLabel)
	}
}

func TestResizeAndSetFramingRPCs(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := tile.Id

	resized, err := cl.MoveTile(ctx, claimtest.Claimed(t, cl, &gridwellv1.MoveTileRequest{
		TileId: id, GridId: root, X: 0, Y: 0, W: 2, H: 2,
	}))
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
		t.Fatalf("set door view: %v", err)
	}
	if tile.ViewCx != 7 || tile.ViewCy != 8 || tile.ViewZoom != 1.5 {
		t.Errorf("after set door view: %+v", tile)
	}
}

func TestSetTextScrollRPC(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("hi"))
	if err != nil {
		t.Fatalf("create text: %v", err)
	}
	id := tile.Id

	tile, err = cl.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: id, Tile: &gridwellv1.Tile{Kind: rpc.KindText, TextX: 1, TextY: 2, TextW: 3, TextH: 4, TextMode: rpc.TextModeRendered}})
	if err != nil {
		t.Fatalf("set text scroll: %v", err)
	}
	if tile.TextX != 1 || tile.TextY != 2 || tile.TextW != 3 || tile.TextH != 4 {
		t.Errorf("after set text scroll: %+v", tile)
	}
	if tile.TextMode != rpc.TextModeRendered {
		t.Errorf("text_mode = %q, want %q", tile.TextMode, rpc.TextModeRendered)
	}
}

func TestDeleteTileRPC(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()
	tile, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := cl.DeleteTile(ctx, claimtest.Claimed(t, cl, &gridwellv1.DeleteTileRequest{TileId: tile.Id})); err != nil {
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
	tile, err = cl.WriteContent(ctx, tile.Id, rpc.ContentBasis{Version: tile.Version}, []byte("v2"))
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

func TestCopyAndMoveRPCs(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()
	tile, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cp, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     tile.Id,
		DestGridId: root, X: 5, Y: 5,
	})
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	moved, err := cl.MoveTile(ctx, claimtest.Claimed(t, cl, &gridwellv1.MoveTileRequest{
		TileId: cp.Id,
		GridId: root, X: 8, Y: 8, W: cp.W, H: cp.H,
	}))
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

	if _, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 0, Y: 0, W: 2, H: 2}}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Overlap → FailedPrecondition.
	_, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 1, Y: 1, W: 1, H: 1}})
	if got := errCode(err); got != connect.CodeFailedPrecondition {
		t.Errorf("overlap: code %v, want FailedPrecondition", got)
	}

	// A create into a grid that doesn't exist → InvalidArgument (grid_id is
	// the authoritative location; there is no descent path).
	pUUID, _, _ := rpc.SplitID(root)
	_, err = cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: pUUID + "/999999", Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 10, Y: 10, W: 1, H: 1}})
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
	if _, err := cl.WriteContent(ctx, tile.Id, rpc.ContentBasis{Version: good}, []byte("v2")); err != nil {
		t.Fatalf("first update: %v", err)
	}
	// Retry with stale claimed version.
	_, err = cl.WriteContent(ctx, tile.Id, rpc.ContentBasis{Version: good}, []byte("v3"))
	if got := errCode(err); got != connect.CodeFailedPrecondition {
		t.Errorf("stale version: code %v, want FailedPrecondition", got)
	}
}

// TestListPlugins: the + menu source lists home, then the files, with their
// labels.
func TestListPlugins(t *testing.T) {
	f := newFilesServer(t)
	list, err := f.cl.Handshake(context.Background())
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	plugins := list.Plugins
	// Home, then the files.
	if len(plugins) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(plugins), plugins)
	}
	if plugins[0].Label != "home" {
		t.Errorf("row 0 = %+v, want the home row", plugins[0])
	}
	// Home advertises its qualified root grid id (for click-enter).
	if !strings.HasPrefix(plugins[0].RootGridId, plugins[0].Uuid+"/") {
		t.Errorf("row 0 root_grid_id = %q, want %q prefix", plugins[0].RootGridId, plugins[0].Uuid)
	}
	// The home grid is stamped with a qualified scratch grid (the
	// ephemeral-url target), distinct from itself; the row carries none.
	home, err := f.cl.GetGrid(context.Background(), plugins[0].RootGridId)
	if err != nil {
		t.Fatalf("GetGrid(home): %v", err)
	}
	if s := home.GetGrid().GetScratchGridId(); !strings.HasPrefix(s, plugins[0].Uuid+"/") || s == plugins[0].RootGridId {
		t.Errorf("home scratch_grid_id = %q, want %q-prefixed and not the root", s, plugins[0].Uuid)
	}
	if plugins[1].Uuid != rpc.FilesOwner(f.node) || plugins[1].Label != "files" {
		t.Errorf("row 1 = %+v, want the files", plugins[1])
	}
}

// homeScratchOf reads the scratch grid stamped on the home row's grid, the
// one place the fact is served.
func homeScratchOf(t *testing.T, cl *rpc.Client, home *gridwellv1.PluginInfo) string {
	t.Helper()
	g, err := cl.GetGrid(context.Background(), home.RootGridId)
	if err != nil {
		t.Fatalf("GetGrid(home): %v", err)
	}
	if g.GetGrid().GetScratchGridId() == "" {
		t.Fatal("home grid carries no scratch grid")
	}
	return g.GetGrid().GetScratchGridId()
}

// TestCreateScratchURLRoutes: creating a url tile whose grid is home's
// qualified scratch grid is an ephemeral visit — it routes path-free into the
// scratch grid (no descent path leads there) and the tile carries link=false
// (it's owned content, just off-grid), with the typed URL. This proves the
// whole client→server→home path-free path the "descend into a url" feature uses.
func TestCreateScratchURLRoutes(t *testing.T) {
	cl := newFilesServer(t).cl
	ctx := context.Background()
	plugins, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	scratch := homeScratchOf(t, cl, plugins.Plugins[0])
	// Empty path + scratch grid: a normal create here would fail path validation
	// (the scratch grid is off-grid); the scratch route bypasses it.
	tile, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: scratch, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1, UrlString: "https://example.com/ephemeral"}})
	if err != nil {
		t.Fatalf("create ephemeral url into scratch: %v", err)
	}
	if tile.Kind != rpc.KindURL || tile.UrlString != "https://example.com/ephemeral" {
		t.Errorf("scratch tile = %+v, want a url tile with the typed URL", tile)
	}
	if tile.Link {
		t.Error("an ephemeral url tile is owned content (off-grid), not a link")
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

// TestDirectoryGridCarriesHomeScratch: a directory's grid, whose namespace
// declares no scratch grid of its own, carries the node's home scratch grid
// instead. The grid is the carrier because it chains through connections;
// the stamp is where the client learns that a link clicked in a rendered
// document inside a directory has somewhere to land as an ephemeral visit.
// Without it the visit could not open.
func TestDirectoryGridCarriesHomeScratch(t *testing.T) {
	f := newFilesServer(t)
	ctx := context.Background()
	hs, err := f.cl.Handshake(ctx)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	homeScratch := homeScratchOf(t, f.cl, hs.Plugins[0])
	g, err := f.cl.GetGrid(ctx, f.grid)
	if err != nil {
		t.Fatalf("GetGrid files root: %v", err)
	}
	if g.Grid.ScratchGridId != homeScratch {
		t.Fatalf("directory grid scratch_grid_id = %q, want home's %q", g.Grid.ScratchGridId, homeScratch)
	}
	// The stamp must not be a dangling pointer: a visit created against it
	// routes into home, exactly what a link click does.
	tile, err := f.cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: g.Grid.ScratchGridId, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1, UrlString: "https://example.com/visit"}})
	if err != nil {
		t.Fatalf("create ephemeral url into stamped scratch: %v", err)
	}
	if tile.Kind != rpc.KindURL {
		t.Errorf("kind = %q, want url", tile.Kind)
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

	want := mkFraming(5.37, -7.125, 0.1)

	// The door row: a door tile.
	door, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 3, Y: 3, W: 3, H: 5}})
	if err != nil {
		t.Fatal(err)
	}
	set, err := cl.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		TileId: door.Id, Cx: want.Cx(), Cy: want.Cy(), Zoom: want.Zoom(),
	})
	if err != nil {
		t.Fatalf("SetFraming(door): %v", err)
	}
	if got := rpc.ViewOf(set.ViewCx, set.ViewCy, set.ViewZoom); got != rpc.Saved(want) {
		t.Errorf("the write's own response = %+v, want %+v", got, want)
	}
	// Read it back the way the client actually reads a door: through
	// the grid it lives in.
	g, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tile := range g.Tiles {
		if tile.Id != door.Id {
			continue
		}
		found = true
		if got := rpc.ViewOf(tile.ViewCx, tile.ViewCy, tile.ViewZoom); got != rpc.Saved(want) {
			t.Errorf("door framing read back = %+v, want %+v", got, want)
		}
	}
	if !found {
		t.Fatal("the door is not in the grid it was created in")
	}

	// The root row: the same verb, the same shape, no door. It reads
	// back through the handshake, where a root's framing rides.
	if _, err := cl.SetFraming(ctx, &gridwellv1.SetFramingRequest{RootGridId: root, Cx: want.Cx(), Cy: want.Cy(), Zoom: want.Zoom()}); err != nil {
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
		if got := rpc.ViewOf(pl.RootViewCx, pl.RootViewCy, pl.RootViewZoom); got != rpc.Saved(want) {
			t.Errorf("root framing read back = %+v, want %+v", got, want)
		}
	}
	if !rootRow {
		t.Fatalf("no handshake row for the root grid %s", root)
	}

	// And the guiding rule: re-writing the SAME framing changes nothing.
	if _, err := cl.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		TileId: door.Id, Cx: want.Cx(), Cy: want.Cy(), Zoom: want.Zoom(),
	}); err != nil {
		t.Fatalf("SetFraming(door, again): %v", err)
	}
	again, err := cl.GetTile(ctx, door.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := rpc.ViewOf(again.ViewCx, again.ViewCy, again.ViewZoom); got != rpc.Saved(want) {
		t.Errorf("rewriting the same framing moved it: %+v, want %+v", got, want)
	}
	if again.Version != set.Version {
		t.Errorf("a framing write bumped version %d → %d", set.Version, again.Version)
	}
}
