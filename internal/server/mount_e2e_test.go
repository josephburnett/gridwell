package server

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// TestSecondDBMountE2E proves the federated model end to end: with the server
// holding no native state, a SECOND Gridwell DB registered as a localdb plugin
// can be mounted as a cross-plugin well in the primary, descended into, and
// edited — and its tiles live in the second DB, isolated from the primary.
func TestSecondDBMountE2E(t *testing.T) {
	ctx := context.Background()

	// Primary DB, registered as the primary localdb plugin.
	st1, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st1.Close() })
	reg := plugin.NewRegistry()
	primaryUUID, root := registerPrimaryLocaldb(t, reg, st1)

	// Second DB, registered as another localdb plugin under its own uuid.
	st2, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	secondUUID, err := st2.PluginUUID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if secondUUID == primaryUUID {
		t.Fatal("two stores produced the same plugin uuid")
	}
	client2, closer2, err := plugin.ServeInProcess(local.New(st2, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer2)
	reg.Register(secondUUID, "home", client2, nil)
	secondBareRoot, err := st2.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secondRoot := secondUUID + "/" + secondBareRoot

	srv := mustNew(t, reg, Config{})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	// Mount the second DB: an exit well in the primary root whose child is the
	// second plugin's root grid.
	mount, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
		ChildGridID: secondRoot, Label: "second",
	})
	if err != nil {
		t.Fatalf("mount CreateWell: %v", err)
	}
	if mount.ChildGridID != secondRoot {
		t.Fatalf("mount child = %q, want %q", mount.ChildGridID, secondRoot)
	}
	if u, _, _ := rpc.SplitID(mount.ID); u != primaryUUID {
		t.Errorf("mount well lives in %q, want primary", u)
	}

	// Descend: GetGrid on the mount's child routes to the second plugin.
	g, err := cl.GetGrid(ctx, mount.ChildGridID)
	if err != nil {
		t.Fatalf("GetGrid second root: %v", err)
	}
	if len(g.Tiles) != 0 {
		t.Fatalf("second DB root should start empty, got %d tiles", len(g.Tiles))
	}

	// Create a text tile inside the second DB (descend path through the mount).
	txt, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: mount.ChildGridID,
		X:      0, Y: 0, W: 1, H: 1, Data: []byte("# in second"),
	})
	if err != nil {
		t.Fatalf("CreateText in second DB: %v", err)
	}
	if u, _, _ := rpc.SplitID(txt.ID); u != secondUUID {
		t.Errorf("text tile lives in %q, want second DB %q", u, secondUUID)
	}

	// Content routes to the second plugin.
	body, _, _, err := cl.ReadContent(ctx, txt.ID)
	if err != nil {
		t.Fatalf("GetTileContent: %v", err)
	}
	if !strings.Contains(string(body), "in second") {
		t.Errorf("content = %q, want it to mention 'in second'", body)
	}

	// Isolation: the tile appears in the second DB's grid, never in the primary
	// root (which holds only the mount well).
	g2, err := cl.GetGrid(ctx, mount.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	if len(g2.Tiles) != 1 || g2.Tiles[0].ID != txt.ID {
		t.Errorf("second DB grid = %+v, want just the text tile", g2.Tiles)
	}
	gp, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tile := range gp.Tiles {
		if tile.ID == txt.ID {
			t.Error("second DB's tile leaked into the primary root grid")
		}
	}
}
