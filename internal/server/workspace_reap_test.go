package server

import (
	"context"
	"fmt"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/claimtest"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// TestDeleteWorkspaceReapsItsEphemerals: a workspace tile's layout
// blob is the ONLY record of the workspace's ephemeral leaves (scratch-grid
// tiles). Destroying the workspace tile closes what the arrangement held,
// exactly like closing a pane does: each referenced SCRATCH tile goes to the
// trash. NON-scratch tiles are content the workspace merely VIEWS and are
// not touched.
func TestDeleteWorkspaceReapsItsEphemerals(t *testing.T) {
	ctx := context.Background()
	reg := &Sources{}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	node, root := registerPrimaryLocaldb(t, reg, st)
	srv := mustNew(t, reg, Config{ID: node})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	pt, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindWorkspace, X: 0, Y: 0, W: 2, H: 2, AltText: "ops"}})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
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
	if _, err := cl.WriteContent(ctx, pt.Id, rpc.ContentBasis{Version: pt.Version}, []byte(layout)); err != nil {
		t.Fatalf("SetWorkspaceLayout: %v", err)
	}

	if _, err := cl.DeleteTile(ctx, claimtest.Claimed(t, cl, &gridwellv1.DeleteTileRequest{TileId: pt.Id})); err != nil {
		t.Fatalf("DeleteTile(workspace): %v", err)
	}

	// The first delete PARKS the workspace in home's trash
	// Its ephemerals stay alive so a restore comes back whole.
	if _, err := cl.GetTile(ctx, eph.Id); err != nil {
		t.Fatalf("a trashed workspace must keep its ephemeral shell: %v", err)
	}
	if _, err := cl.GetTile(ctx, pt.Id); err != nil {
		t.Fatalf("trashed workspace tile must still read: %v", err)
	}
	// The second delete (inside the trash) DESTROYS — and only then does
	// the router reap what the arrangement owned.
	if _, err := cl.DeleteTile(ctx, claimtest.Claimed(t, cl, &gridwellv1.DeleteTileRequest{TileId: pt.Id})); err != nil {
		t.Fatalf("DeleteTile(workspace, in trash): %v", err)
	}
	if !trashedVisit(t, cl, scratch, eph.Id) {
		t.Error("the ephemeral shell was not closed into the trash with the workspace tile's destroy")
	}
	if _, err := cl.GetTile(ctx, txt.Id); err != nil {
		t.Errorf("viewed content was deleted with the workspace: %v", err)
	}

	// A workspace tile with an UNREADABLE blob must still delete without touching
	// anything (never guess at what to reap), mirroring the read-only latch.
	pt2, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindWorkspace, X: 5, Y: 5, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	eph2, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: scratch, Tile: &gridwellv1.Tile{Kind: rpc.KindShell, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.WriteContent(ctx, pt2.Id, rpc.ContentBasis{Version: pt2.Version}, []byte(`{"v":999,"root":{}}`)); err != nil {
		t.Fatalf("SetWorkspaceLayout (future version): %v", err)
	}
	if _, err := cl.DeleteTile(ctx, claimtest.Claimed(t, cl, &gridwellv1.DeleteTileRequest{TileId: pt2.Id})); err != nil {
		t.Fatalf("DeleteTile(workspace, unreadable blob): %v", err)
	}
	if _, err := cl.GetTile(ctx, pt2.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.DeleteTile(ctx, claimtest.Claimed(t, cl, &gridwellv1.DeleteTileRequest{TileId: pt2.Id})); err != nil {
		t.Fatalf("DeleteTile(workspace, unreadable blob, in trash): %v", err)
	}
	if got, err := cl.GetTile(ctx, eph2.Id); err != nil || got.GridId != scratch {
		t.Errorf("unreadable blob must reap NOTHING (never guess), but the scratch tile moved: %+v, %v", got, err)
	}
}

// A workspace tile's destroy closes an ephemeral shell it held into the trash
// with its tmux running, as any deleted shell keeps its tmux; destroying the
// shell from the trash ends it.
func TestAReapedShellKeepsItsTmuxInTheTrash(t *testing.T) {
	ctx := context.Background()
	f := newShellAPIFixture(t, Config{ID: "lnode1"})
	g, err := f.cl.GetGrid(ctx, f.root)
	if err != nil {
		t.Fatal(err)
	}
	scratch := g.Grid.ScratchGridId
	eph, err := f.cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: scratch,
		Tile: &gridwellv1.Tile{Kind: rpc.KindShell, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	pt, err := f.cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: f.root,
		Tile: &gridwellv1.Tile{Kind: rpc.KindWorkspace, X: 0, Y: 0, W: 2, H: 2}})
	if err != nil {
		t.Fatal(err)
	}
	layout := fmt.Sprintf(`{"v":1,"root":{"pane":{"id":"p1","anchor":%q,"cx":0.5,"cy":0.5,"zoom":1,"text_focus":%q}},"focus":"p1"}`,
		f.root, eph.Id)
	if _, err := f.cl.WriteContent(ctx, pt.Id, rpc.ContentBasis{Version: pt.Version}, []byte(layout)); err != nil {
		t.Fatal(err)
	}
	destroyWorkspace(t, f.cl, pt.Id)
	if !trashedVisit(t, f.cl, scratch, eph.Id) {
		t.Fatal("the reaped shell is not in the trash")
	}
	if k := f.fake.Killed(); len(k) != 0 {
		t.Fatalf("killed %v, want the trashed shell's tmux running", k)
	}
	if _, err := f.cl.DeleteTile(ctx, claimtest.Claimed(t, f.cl, &gridwellv1.DeleteTileRequest{TileId: eph.Id})); err != nil {
		t.Fatal(err)
	}
	if k := f.fake.Killed(); len(k) != 1 {
		t.Fatalf("killed %v after the destroy from the trash, want its tmux", k)
	}
}
