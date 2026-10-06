package server

import (
	"context"
	"fmt"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// TestDeletePaneTileReapsItsEphemerals: a pane tile's layout
// blob is the ONLY record of the workspace's ephemeral leaves (scratch-grid
// tiles). Deleting the pane tile deletes only the arrangement — but must
// terminate what the arrangement owns, exactly like closing a pane does:
// each referenced SCRATCH tile is deleted (which kills its tmux session via
// the existing DeleteTile→shell.Kill chain). Referenced NON-scratch tiles
// are content the workspace merely VIEWS and must survive — deleting a
// workspace never deletes data.
func TestDeletePaneTileReapsItsEphemerals(t *testing.T) {
	ctx := context.Background()
	reg := plugin.NewRegistry()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, root := registerPrimaryLocaldb(t, reg, st)
	srv := mustNew(t, reg, Config{})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	pt, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindPane, X: 0, Y: 0, W: 2, H: 2, AltText: "ops"}})
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	g, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	scratch := g.Grid.ScratchGridId
	if scratch == "" {
		t.Fatal("no scratch grid advertised")
	}
	eph, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: scratch, Tile: &gridwellv1.Tile{Kind: rpc.KindShell, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatalf("CreateShell (scratch): %v", err)
	}
	txt, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 3, Y: 3, W: 1, H: 1}}, []byte("# viewed"))
	if err != nil {
		t.Fatalf("CreateText: %v", err)
	}

	// The workspace's layout: one leaf descended into the ephemeral shell,
	// one into the viewed text tile (LayoutV1, the same bytes the client
	// persister writes).
	layout := fmt.Sprintf(`{"v":1,"root":{"split":{"dir":"v","ratio":0.5,`+
		`"a":{"pane":{"id":"p1","anchor":%q,"cx":0.5,"cy":0.5,"zoom":1,"text_focus":%q}},`+
		`"b":{"pane":{"id":"p2","anchor":%q,"cx":0.5,"cy":0.5,"zoom":1,"text_focus":%q}}}},"focus":"p1"}`,
		root, eph.Id, root, txt.Id)
	if _, err := cl.WriteContent(ctx, pt.Id, pt.Version, []byte(layout)); err != nil {
		t.Fatalf("SetPaneLayout: %v", err)
	}

	if _, err := cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: pt.Id}); err != nil {
		t.Fatalf("DeleteTile(pane): %v", err)
	}

	// The first delete PARKS the workspace in the local plugin's trash
	// Its ephemerals stay alive so a restore comes back whole.
	if _, err := cl.GetTile(ctx, eph.Id); err != nil {
		t.Fatalf("a trashed workspace must keep its ephemeral shell: %v", err)
	}
	if _, err := cl.GetTile(ctx, pt.Id); err != nil {
		t.Fatalf("trashed pane tile must still read: %v", err)
	}
	// The second delete (inside the trash) DESTROYS — and only then does
	// the router reap what the arrangement owned.
	if _, err := cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: pt.Id}); err != nil {
		t.Fatalf("DeleteTile(pane, in trash): %v", err)
	}
	if _, err := cl.GetTile(ctx, eph.Id); err == nil {
		t.Error("ephemeral scratch tile survived the pane-tile destroy — its shell would leak until the boot sweep")
	}
	if _, err := cl.GetTile(ctx, txt.Id); err != nil {
		t.Errorf("viewed content was deleted with the workspace: %v", err)
	}

	// A pane tile with an UNREADABLE blob must still delete without touching
	// anything (never guess at what to reap), mirroring the read-only latch.
	pt2, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindPane, X: 5, Y: 5, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	eph2, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: scratch, Tile: &gridwellv1.Tile{Kind: rpc.KindShell, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.WriteContent(ctx, pt2.Id, pt2.Version, []byte(`{"v":999,"root":{}}`)); err != nil {
		t.Fatalf("SetPaneLayout (future version): %v", err)
	}
	if _, err := cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: pt2.Id}); err != nil {
		t.Fatalf("DeleteTile(pane, unreadable blob): %v", err)
	}
	if _, err := cl.GetTile(ctx, pt2.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: pt2.Id}); err != nil {
		t.Fatalf("DeleteTile(pane, unreadable blob, in trash): %v", err)
	}
	if _, err := cl.GetTile(ctx, eph2.Id); err != nil {
		t.Errorf("unreadable blob must reap NOTHING (never guess), but the scratch tile is gone: %v", err)
	}
}

// A pane tile's destroy that reaps an ephemeral shell whose session will not
// stop says so on its answer, as a destroyed shell row's own delete does.
func TestAReapThatLeavesASessionRunningSaysSo(t *testing.T) {
	ctx := context.Background()
	f := newShellDoorFixture(t, Config{ID: "lnode1"})
	g, err := f.cl.GetGrid(ctx, f.root)
	if err != nil {
		t.Fatal(err)
	}
	eph, err := f.cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: g.Grid.ScratchGridId,
		Tile: &gridwellv1.Tile{Kind: rpc.KindShell, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	pt, err := f.cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: f.root,
		Tile: &gridwellv1.Tile{Kind: rpc.KindPane, X: 0, Y: 0, W: 2, H: 2}})
	if err != nil {
		t.Fatal(err)
	}
	layout := fmt.Sprintf(`{"v":1,"root":{"pane":{"id":"p1","anchor":%q,"cx":0.5,"cy":0.5,"zoom":1,"text_focus":%q}},"focus":"p1"}`,
		f.root, eph.Id)
	if _, err := f.cl.WriteContent(ctx, pt.Id, pt.Version, []byte(layout)); err != nil {
		t.Fatal(err)
	}
	f.fake.KillErr = fmt.Errorf("tmux: server exited unexpectedly")
	if _, err := f.cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: pt.Id}); err != nil {
		t.Fatal(err)
	}
	resp, err := f.cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: pt.Id})
	if err != nil {
		t.Fatalf("destroy = %v, want it to land", err)
	}
	if !strings.Contains(resp.SessionLeft, "server exited unexpectedly") {
		t.Fatalf("session_left = %q, want the reaped shell's reason", resp.SessionLeft)
	}
}
