package server

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// TestSecondDBDoorE2E proves the federated model end to end: with the server
// holding no native state, a second Gridwell DB registered as its own namespace
// can be reached through a cross-plugin door in the primary, descended into, and
// edited — and its tiles live in the second DB, isolated from the primary.
func TestSecondDBDoorE2E(t *testing.T) {
	ctx := context.Background()

	// The primary DB, registered as the primary namespace.
	st1, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st1.Close() })
	reg := plugin.NewRegistry()
	primaryUUID, root := registerPrimaryLocaldb(t, reg, st1)

	// A second DB, registered as another namespace under its own uuid.
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
	client2 := local.New(st2, nil)
	reg.Register(secondUUID, "home", client2, nil)
	secondBareRoot, err := st2.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secondRoot := secondUUID + "/" + secondBareRoot

	srv := mustNew(t, reg, Config{})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	// Link the second DB: a door to another namespace in the primary root whose child is the
	// second plugin's root grid.
	farDoor, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 0, Y: 0, W: 1, H: 1, ChildGridId: secondRoot, AltText: "second"}})
	if err != nil {
		t.Fatalf("farDoor CreateDoor: %v", err)
	}
	if farDoor.ChildGridId != secondRoot {
		t.Fatalf("farDoor child = %q, want %q", farDoor.ChildGridId, secondRoot)
	}
	if u, _, _ := rpc.SplitID(farDoor.Id); u != primaryUUID {
		t.Errorf("door lives in %q, want primary", u)
	}

	// Descend: GetGrid on the door's child routes to the second plugin.
	g, err := cl.GetGrid(ctx, farDoor.ChildGridId)
	if err != nil {
		t.Fatalf("GetGrid second root: %v", err)
	}
	if len(g.Tiles) != 0 {
		t.Fatalf("second DB root should start empty, got %d tiles", len(g.Tiles))
	}

	// Create a text tile inside the second DB (descend path through the door).
	txt, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: farDoor.ChildGridId, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("# in second"))
	if err != nil {
		t.Fatalf("CreateText in second DB: %v", err)
	}
	if u, _, _ := rpc.SplitID(txt.Id); u != secondUUID {
		t.Errorf("text tile lives in %q, want second DB %q", u, secondUUID)
	}

	// Content routes to the second plugin.
	body, _, _, err := cl.ReadContent(ctx, txt.Id)
	if err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	if !strings.Contains(string(body), "in second") {
		t.Errorf("content = %q, want it to mention 'in second'", body)
	}

	// Isolation: the tile appears in the second DB's grid, never in the primary
	// root (which holds only the door).
	g2, err := cl.GetGrid(ctx, farDoor.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	if len(g2.Tiles) != 1 || g2.Tiles[0].Id != txt.Id {
		t.Errorf("second DB grid = %+v, want just the text tile", g2.Tiles)
	}
	gp, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tile := range gp.Tiles {
		if tile.Id == txt.Id {
			t.Error("second DB's tile leaked into the primary root grid")
		}
	}
}
