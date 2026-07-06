package server

import (
	"context"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/store"
)

// nodeGridServer wires a server WITH a node id and two localdb plugins,
// labeled "personal" and "work", and returns a connect client plus the node's
// qualified root grid id.
func nodeGridServer(t *testing.T) (cl *rpc.Client, nodeRoot, uuidA, rootA string) {
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
	clientB, closerB, err := plugin.ServeInProcess(localdb.New(stB, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closerB)
	reg.Register(uuidB, "localdb", clientB, nil)
	reg.SetLabel(uuidB, "work")

	srv := New(reg, Config{NodeID: "node1"})
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
	// A plugin's own grid IS writable — per grid, not per local plugin entry.
	pg, err := cl.GetGrid(ctx, rootA)
	if err != nil {
		t.Fatal(err)
	}
	if !pg.Grid.Writable {
		t.Error("a localdb root must report writable=true")
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
	body, err := cl.GetTileContent(ctx, txt.ID)
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
	nodeUUID, nodeRoot, err := cl.NodeIdentity(context.Background())
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if nodeUUID != "node1" || nodeRoot != "node1/0" {
		t.Errorf("node identity = %q %q, want node1 node1/0", nodeUUID, nodeRoot)
	}
}
