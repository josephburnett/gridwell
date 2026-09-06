package server

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// reapFixture stands a node up with a root grid, a scratch grid and a pane
// tile, and returns a delete-the-pane-tile-for-real closure. Both phases of
// the owner test need the same setup.
func reapFixture(t *testing.T) (cl *rpc.Client, root, scratch string) {
	t.Helper()
	reg := plugin.NewRegistry()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, root = registerPrimaryLocaldb(t, reg, st)
	srv := mustNew(t, reg, Config{})
	hs := serveWeb(t, srv)
	cl = rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	g, err := cl.GetGrid(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	scratch = g.Grid.ScratchGridID
	if scratch == "" {
		t.Fatal("no scratch grid advertised")
	}
	return cl, root, scratch
}

// destroyPane deletes a pane tile twice: once into the trash, once for real,
// which is when the router reaps what the arrangement owned.
func destroyPane(t *testing.T, cl *rpc.Client, tileID string) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := cl.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: tileID}); err != nil {
			t.Fatalf("DeleteTile(pane) %d: %v", i+1, err)
		}
	}
}

// TestReapReadsTheSameFieldTheSweepProtects: "which content tiles does a pane
// blob reference" has one owner, api/panelayout.TextFocusIDs — the `text_focus`
// projection the boot sweep's protection set reads. The delete-time reap must
// answer from that same field, not from a second derivation of its own.
//
// The blob here is one no encoder writes: a `place` frame stack whose top
// frame is a content descent on the scratch shell, with `text_focus` omitted.
// A reap that re-derives the content id from `place` reaps that shell; the
// boot sweep, reading `text_focus`, never protected it in the first place. The
// two answers must not differ, and the field the sweep reads is the one that
// decides.
func TestReapReadsTheSameFieldTheSweepProtects(t *testing.T) {
	ctx := context.Background()
	cl, root, scratch := reapFixture(t)

	pt, err := cl.CreatePane(ctx, &rpc.CreatePaneRequest{
		GridID: root, X: 0, Y: 0, W: 2, H: 2, Label: "ops",
	})
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	eph, err := cl.CreateShell(ctx, &rpc.CreateShellRequest{GridID: scratch, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatalf("CreateShell (scratch): %v", err)
	}

	layout := fmt.Sprintf(`{"v":1,"root":{"pane":{"id":"p1","cx":0,"cy":0,"zoom":1,`+
		`"place":[{"g":%q},{"d":%q,"c":true}]}},"focus":"p1"}`, root, eph.ID)
	if _, err := cl.WriteContent(ctx, pt.ID, pt.Version, []byte(layout)); err != nil {
		t.Fatalf("SetPaneLayout: %v", err)
	}

	destroyPane(t, cl, pt.ID)

	if _, err := cl.GetTile(ctx, eph.ID); err != nil {
		t.Errorf("the reap derived a reference the boot sweep's protection set "+
			"does not see — two decoders of the same blob: %v", err)
	}
}

// TestReapFindsEncoderWrittenReferences: the other half — a blob written by
// the real client encoder, for a place deep enough that the encoder writes
// BOTH the full `place` stack and the `text_focus` projection. The reap still
// finds exactly the referenced scratch tile, and leaves viewed content alone.
// This is the seam: bytes written by client/pane, read by the server.
func TestReapFindsEncoderWrittenReferences(t *testing.T) {
	ctx := context.Background()
	cl, root, scratch := reapFixture(t)

	pt, err := cl.CreatePane(ctx, &rpc.CreatePaneRequest{
		GridID: root, X: 0, Y: 0, W: 2, H: 2, Label: "ops",
	})
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
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

	// A place with a namespace crossing below its top level: the projection
	// cannot hold it, so the encoder writes `place` as well as `text_focus`.
	tr := pane.NewTree()
	p := tr.FocusedPane()
	p.Stack = pane.NewStack(root)
	p.Push(pane.Frame{GridID: "other/1", Door: root, Zoom: 1})
	p.Push(pane.Frame{Door: eph.ID, Content: true})
	second, err := tr.Split(pane.Vertical)
	if err != nil {
		t.Fatal(err)
	}
	second.Stack = pane.StackAt(root, nil, txt.ID)
	second.Zoom = 1

	data, skipped, err := pane.EncodeLayout(tr, nil)
	if err != nil {
		t.Fatalf("EncodeLayout: %v", err)
	}
	if len(skipped) != 0 {
		t.Fatalf("encoder skipped leaves: %v", skipped)
	}
	if _, err := cl.WriteContent(ctx, pt.ID, pt.Version, data); err != nil {
		t.Fatalf("SetPaneLayout: %v", err)
	}

	destroyPane(t, cl, pt.ID)

	if _, err := cl.GetTile(ctx, eph.ID); err == nil {
		t.Error("ephemeral scratch tile survived the destroy — its shell would leak until the boot sweep")
	}
	if _, err := cl.GetTile(ctx, txt.ID); err != nil {
		t.Errorf("viewed content was reaped with the workspace: %v", err)
	}
}
