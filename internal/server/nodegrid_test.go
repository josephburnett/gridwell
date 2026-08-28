package server

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// nodeGridServer wires a server WITH a node id and two localdb plugins,
// labeled "personal" and "work", and returns a connect client plus the node's
// qualified root grid id.
func nodeGridServer(t *testing.T) (cl *rpc.Client, nodeRoot, uuidA, rootA string) {
	t.Helper()
	return nodeGridServerAt(t, "")
}

// nodeGridServerAt pins the node state file, for arrangement-persistence
// tests.
func nodeGridServerAt(t *testing.T, statePath string) (cl *rpc.Client, nodeRoot, uuidA, rootA string) {
	t.Helper()
	ctx := context.Background()
	reg := plugin.NewRegistry()

	stA, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stA.Close() })
	uuidA, rootA = registerPrimaryLocaldb(t, reg, stA)
	reg.SetLabel(uuidA, "personal")

	stB, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stB.Close() })
	uuidB, err := stB.PluginUUID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clientB, closerB, err := plugin.ServeInProcess(local.New(stB, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closerB)
	reg.Register(uuidB, "home", clientB, nil)
	reg.SetLabel(uuidB, "work")

	srv := mustNew(t, reg, Config{NodeID: "node1", NodeStatePath: statePath})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON()), "node1/0", uuidA, rootA
}

func TestNodeGridListsPluginsAsLinkTiles(t *testing.T) {
	cl, nodeRoot, uuidA, rootA := nodeGridServer(t)
	ctx := context.Background()

	g, err := cl.GetGrid(ctx, nodeRoot)
	if err != nil {
		t.Fatalf("GetGrid(node): %v", err)
	}
	if len(g.Tiles) != 2 {
		t.Fatalf("node grid has %d tiles, want 2 (one per plugin)", len(g.Tiles))
	}
	first := g.Tiles[0]
	if first.ID != "node1/"+uuidA {
		t.Errorf("tile id = %q, want node1/%s (the plugin uuid IS the tile id)", first.ID, uuidA)
	}
	if first.AltText != "personal" {
		t.Errorf("tile label = %q, want the config name", first.AltText)
	}
	if first.ChildGridID != rootA {
		t.Errorf("tile child = %q, want the plugin's qualified root %q", first.ChildGridID, rootA)
	}
	if !first.Reference {
		t.Error("a plugin tile is a LINK (dashed) — Reference must be true")
	}
	if first.Kind != rpc.KindWell {
		t.Errorf("tile kind = %q, want well", first.Kind)
	}

	// The node grid itself is read-only — the palette must not offer creates.
	if g.Grid.Writable {
		t.Error("node grid must report writable=false")
	}
	// A plugin's own grid IS writable — per grid, not per local plugin entry —
	// and carries the plugin's scratch grid, qualified (the ephemeral-url
	// fact rides ON the grid so it survives transit mounts, issue #59).
	pg, err := cl.GetGrid(ctx, rootA)
	if err != nil {
		t.Fatal(err)
	}
	if !pg.Grid.Writable {
		t.Error("a localdb root must report writable=true")
	}
	if pg.Grid.ScratchGridID == "" || rpc.UUIDOf(pg.Grid.ScratchGridID) != uuidA {
		t.Errorf("scratch on the grid = %q, want the plugin's qualified scratch", pg.Grid.ScratchGridID)
	}
	if g.Grid.SourceKind != "node" {
		t.Errorf("node grid source_kind = %q, want node (drives the mount glyph)", g.Grid.SourceKind)
	}
}

func TestNodeGridDescentReachesThePlugin(t *testing.T) {
	cl, nodeRoot, _, _ := nodeGridServer(t)
	ctx := context.Background()

	g, err := cl.GetGrid(ctx, nodeRoot)
	if err != nil {
		t.Fatal(err)
	}
	// Descend through the first link: its child grid answers, and creating
	// content there lands in the plugin — the node grid is pure routing.
	child := g.Tiles[0].ChildGridID
	txt, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: child, X: 0, Y: 0, W: 1, H: 1, Data: []byte("# through the node grid"),
	})
	if err != nil {
		t.Fatalf("create through node-grid child: %v", err)
	}
	body, _, _, err := cl.ReadContent(ctx, txt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "# through the node grid" {
		t.Errorf("content = %q", body)
	}
}

func TestNodeGridFramingWritebackPersistsRootView(t *testing.T) {
	// Ascending from a plugin root writes the well's framing back via SetTile
	// (face #3 of the guiding rule); on a node-grid tile that maps onto the
	// plugin's own SetRootView, so the framing survives in the plugin's DB
	// and the next GetGrid serves it on the tile.
	cl, nodeRoot, uuidA, _ := nodeGridServer(t)
	ctx := context.Background()

	tileID := "node1/" + uuidA
	if _, err := cl.SetWellView(ctx, &rpc.SetWellViewRequest{
		TileID: tileID, ViewX: 7, ViewY: -3, ViewZoom: 2.5,
	}); err != nil {
		t.Fatalf("SetWellView on node tile: %v", err)
	}
	g, err := cl.GetGrid(ctx, nodeRoot)
	if err != nil {
		t.Fatal(err)
	}
	got := g.Tiles[0]
	if got.ViewX != 7 || got.ViewY != -3 || got.ViewZoom != 2.5 {
		t.Errorf("framing after writeback = (%d,%d,%v), want (7,-3,2.5)", got.ViewX, got.ViewY, got.ViewZoom)
	}
}

func TestNodeGridRefusesContentMutations(t *testing.T) {
	cl, nodeRoot, uuidA, _ := nodeGridServer(t)
	ctx := context.Background()

	if _, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: nodeRoot, X: 5, Y: 5, W: 1, H: 1, Data: []byte("x"),
	}); err == nil {
		t.Error("creating in the node grid succeeded, want read-only refusal")
	}
	if err := cl.DeleteTile(ctx, &rpc.DeleteTileRequest{
		TileID: "node1/" + uuidA, Version: 0,
	}); err == nil {
		t.Error("deleting a plugin tile succeeded, want refusal (plugins are removed via config)")
	}
}

func TestListPluginsCarriesNodeIdentity(t *testing.T) {
	cl, _, _, _ := nodeGridServer(t)
	pl, err := cl.ListPlugins(context.Background())
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if pl.NodeUUID != "node1" || pl.NodeRootGridID != "node1/0" {
		t.Errorf("node identity = %q %q, want node1 node1/0", pl.NodeUUID, pl.NodeRootGridID)
	}
}

func TestNodeGridViewportSurvivesRestart(t *testing.T) {
	// The landing page's own framing is durable: SetRootView mirrors it to
	// the state file, and a fresh server (a restart) serves it back through
	// Info/the node tiles' plugin — things stay as you left them.
	dir := t.TempDir()
	statePath := dir + "/node-view.json"

	build := func() (*rpc.Client, func()) {
		st, err := store.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		reg := plugin.NewRegistry()
		uuidA, _ := registerPrimaryLocaldb(t, reg, st)
		reg.SetLabel(uuidA, "personal")
		srv := mustNew(t, reg, Config{NodeID: "node1", NodeStatePath: statePath})
		hs := httptest.NewServer(srv.Handler())
		cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
		return cl, func() { hs.Close(); _ = st.Close() }
	}

	cl, closer := build()
	if err := cl.SetRootView(context.Background(), &rpc.SetRootViewRequest{
		RootGridID: "node1/0", Cx: 4.5, Cy: -2.25, Zoom: 1.75,
	}); err != nil {
		t.Fatalf("SetRootView(node grid): %v", err)
	}
	closer()

	// "Restart": a brand-new server against the same state file.
	cl2, closer2 := build()
	defer closer2()
	// The node grid's Info carries the restored viewport (what the client
	// would seed a bookmark-boot from). Read it via ListPlugins is the local
	// plugin list, so go through the plugin directly: GetGrid's tiles use
	// per-PLUGIN views; the node's OWN view rides Info of the node uuid —
	// exercised through SetRootView's read-back pair: write once more with
	// the same values must be a no-op file-wise, and a GetGrid still works.
	g, err := cl2.GetGrid(context.Background(), "node1/0")
	if err != nil {
		t.Fatalf("GetGrid after restart: %v", err)
	}
	if len(g.Tiles) != 1 {
		t.Fatalf("node grid lost its tiles after restart: %+v", g.Tiles)
	}
	// Assert the persisted values round-tripped by reading the state file's
	// owner through the wire: the plugin's Info.
	info, err := nodeInfoViaExport(t, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.RootViewCx != 4.5 || info.RootViewCy != -2.25 || info.RootViewZoom != 1.75 {
		t.Errorf("restored viewport = (%v,%v,%v), want (4.5,-2.25,1.75)",
			info.RootViewCx, info.RootViewCy, info.RootViewZoom)
	}
}

// nodeInfoViaExport reads the node-grid plugin's Info directly (a fresh
// plugin against the state file), the same handshake a mounter sees.
func nodeInfoViaExport(t *testing.T, statePath string) (*pb.InfoResponse, error) {
	t.Helper()
	ng := &nodeGrid{reg: plugin.NewRegistry(), info: nil, invalidate: func(string) {}, statePath: statePath}
	ng.loadView()
	return ng.Info(context.Background(), &pb.InfoRequest{})
}

// The launcher stays as you left it (v2, #269): placement persists in
// the node state file, survives a server restart, and unplaced tiles
// keep their config-order default row.
func TestNodeGridPlacementPersists(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "node-view.json")
	cl, nodeRoot, uuidA, _ := nodeGridServerAt(t, statePath)
	ctx := context.Background()

	tileID := "node1/" + uuidA
	if _, err := cl.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: tileID, GridID: nodeRoot, X: 3, Y: -2, W: 2, H: 1,
	}); err != nil {
		t.Fatalf("PlaceTile on the launcher: %v", err)
	}
	g, err := cl.GetGrid(ctx, nodeRoot)
	if err != nil {
		t.Fatal(err)
	}
	var moved, other *rpc.Tile
	for i := range g.Tiles {
		if g.Tiles[i].ID == tileID {
			moved = &g.Tiles[i]
		} else {
			other = &g.Tiles[i]
		}
	}
	if moved == nil || moved.X != 3 || moved.Y != -2 || moved.W != 2 {
		t.Fatalf("placement not served back: %+v", moved)
	}
	if other == nil || other.X != 1 || other.Y != 0 {
		t.Fatalf("unplaced tile left its default row: %+v", other)
	}

	// A fresh plugin over the same state file (the restart) still
	// serves the arrangement.
	ng := &nodeGrid{reg: plugin.NewRegistry(), info: nil, invalidate: func(string) {}, statePath: statePath}
	ng.loadView()
	if pos, ok := ng.view.Tiles[uuidA]; !ok || pos.X != 3 || pos.Y != -2 || pos.W != 2 {
		t.Fatalf("restart lost the arrangement: %+v ok=%v", pos, ok)
	}

	// Content mutations stay refused — rearrangeable is not writable.
	if _, err := cl.CreateText(ctx, &rpc.CreateTextRequest{GridID: nodeRoot, X: 9, Y: 9, W: 1, H: 1}); err == nil {
		t.Fatal("create on the node grid succeeded, want refusal")
	}
}
