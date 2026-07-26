package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/store"
)

// TestDeletePaneTileReapsItsEphemerals (issue #174): a pane tile's layout
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
	srv := New(reg, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	pt, err := cl.CreatePane(ctx, &rpc.CreatePaneRequest{
		GridID: root, X: 0, Y: 0, W: 2, H: 2, Label: "ops",
	})
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	g, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	scratch := g.Grid.ScratchGridID
	if scratch == "" {
		t.Fatal("no scratch grid advertised")
	}
	eph, err := cl.CreateShell(ctx, &rpc.CreateShellRequest{GridID: scratch, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatalf("CreateShell (scratch): %v", err)
	}
	txt, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root, X: 3, Y: 3, W: 1, H: 1, Data: []byte("# viewed"),
	})
	if err != nil {
		t.Fatalf("CreateText: %v", err)
	}

	// The workspace's layout: one leaf descended into the ephemeral shell,
	// one into the viewed text tile (LayoutV1, the same bytes the client
	// persister writes).
	layout := fmt.Sprintf(`{"v":1,"root":{"split":{"dir":"v","ratio":0.5,`+
		`"a":{"pane":{"id":"p1","anchor":%q,"cx":0.5,"cy":0.5,"zoom":1,"text_focus":%q}},`+
		`"b":{"pane":{"id":"p2","anchor":%q,"cx":0.5,"cy":0.5,"zoom":1,"text_focus":%q}}}},"focus":"p1"}`,
		root, eph.ID, root, txt.ID)
	if _, err := cl.WriteContent(ctx, pt.ID, pt.Version, []byte(layout)); err != nil {
		t.Fatalf("SetPaneLayout: %v", err)
	}

	if err := cl.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: pt.ID, Version: pt.Version}); err != nil {
		t.Fatalf("DeleteTile(pane): %v", err)
	}

	if _, err := cl.GetTile(ctx, eph.ID); err == nil {
		t.Error("ephemeral scratch tile survived the pane-tile delete — its shell would leak until the boot sweep")
	}
	if _, err := cl.GetTile(ctx, txt.ID); err != nil {
		t.Errorf("viewed content was deleted with the workspace: %v", err)
	}

	// A pane tile with an UNREADABLE blob must still delete without touching
	// anything (never guess at what to reap), mirroring the read-only latch.
	pt2, err := cl.CreatePane(ctx, &rpc.CreatePaneRequest{GridID: root, X: 5, Y: 5, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	eph2, err := cl.CreateShell(ctx, &rpc.CreateShellRequest{GridID: scratch, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.WriteContent(ctx, pt2.ID, pt2.Version, []byte(`{"v":999,"root":{}}`)); err != nil {
		t.Fatalf("SetPaneLayout (future version): %v", err)
	}
	if err := cl.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: pt2.ID, Version: pt2.Version}); err != nil {
		t.Fatalf("DeleteTile(pane, unreadable blob): %v", err)
	}
	if _, err := cl.GetTile(ctx, eph2.ID); err != nil {
		t.Errorf("unreadable blob must reap NOTHING (never guess), but the scratch tile is gone: %v", err)
	}
}
