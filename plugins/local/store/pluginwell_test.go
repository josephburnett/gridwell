package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// The unconfigured plugin well (issue #251): a childless well carrying the
// uuid of the parameterized plugin whose instance will fill it, turned into
// an ordinary exit-well link by AdoptChildGrid. These tests pin the two
// halves of that lifecycle and the refusals that keep a well's target from
// silently changing.

func createPluginWell(t *testing.T, s *Store, grid string, x int64) *rpc.Tile {
	t.Helper()
	tile, err := s.CreatePluginWell(context.Background(), grid, x, 0, 1, 1, "sshuuid")
	if err != nil {
		t.Fatalf("CreatePluginWell: %v", err)
	}
	return tile
}

func TestCreatePluginWell_ChildlessAndMarked(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	tile := createPluginWell(t, s, root, 0)
	if tile.Kind != rpc.KindWell {
		t.Errorf("kind = %q, want well", tile.Kind)
	}
	if tile.ChildGridID != "" {
		t.Errorf("child grid = %q, want childless until adopted", tile.ChildGridID)
	}
	if tile.ConfigurePluginID != "sshuuid" {
		t.Errorf("configure_plugin_id = %q, want sshuuid", tile.ConfigurePluginID)
	}
	if tile.AltText != "" {
		t.Errorf("alt = %q, want born unnamed (the adopted instance names it)", tile.AltText)
	}
	// Requires the plugin uuid — a childless well with nothing to configure
	// is not a legal shape.
	if _, err := s.CreatePluginWell(context.Background(), root, 5, 0, 1, 1, ""); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("empty configure_plugin_id: err = %v, want ErrInvalidArgument", err)
	}
}

func TestAdoptChildGrid_TurnsThePluginWellIntoALink(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	tile := createPluginWell(t, s, root, 0)

	got, err := s.AdoptChildGrid(context.Background(), &rpc.AdoptChildGridRequest{
		TileID: tile.ID, Version: tile.Version,
		ChildGridID: "sshuuid/conn123", Label: "gpu-box",
		ViewX: 3, ViewY: 4, ViewZoom: 1.25,
	})
	if err != nil {
		t.Fatalf("AdoptChildGrid: %v", err)
	}
	if got.ChildGridID != "sshuuid/conn123" {
		t.Errorf("child grid = %q, want the adopted chain", got.ChildGridID)
	}
	if got.ViewX != 3 || got.ViewY != 4 || got.ViewZoom != 1.25 {
		t.Errorf("view = (%d,%d,%v), want the seeded framing", got.ViewX, got.ViewY, got.ViewZoom)
	}
	if got.AltText != "gpu-box" {
		t.Errorf("alt = %q, want the instance's name as the default label", got.AltText)
	}
	if got.Version != tile.Version+1 {
		t.Errorf("version %d -> %d, want a USER edit to bump by one", tile.Version, got.Version)
	}
	if got.ConfigurePluginID != "sshuuid" {
		t.Errorf("configure_plugin_id = %q, want kept as provenance", got.ConfigurePluginID)
	}
}

func TestAdoptChildGrid_NeverClobbersAUserName(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	tile := createPluginWell(t, s, root, 0)
	id, _ := parseID(tile.ID)
	if err := s.SetTileAlt(context.Background(), id, "my-name", true); err != nil {
		t.Fatal(err)
	}
	renamed, err := s.GetTile(context.Background(), tile.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.AdoptChildGrid(context.Background(), &rpc.AdoptChildGridRequest{
		TileID: tile.ID, Version: renamed.Version, ChildGridID: "sshuuid/c1", Label: "default-name",
	})
	if err != nil {
		t.Fatalf("AdoptChildGrid: %v", err)
	}
	if got.AltText != "my-name" {
		t.Errorf("alt = %q, want the user's rename to survive the default label", got.AltText)
	}
}

func TestAdoptChildGrid_Refusals(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	// A well that already has a child grid: the target never silently
	// changes — this is the class the arm must make unwritable.
	well, err := s.CreateWell(ctx, &rpc.CreateWellRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdoptChildGrid(ctx, &rpc.AdoptChildGridRequest{
		TileID: well.ID, Version: well.Version, ChildGridID: "sshuuid/c1",
	}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("adopt onto a configured well: err = %v, want ErrInvalidArgument", err)
	}

	// Not a well.
	text, err := s.CreateText(ctx, &rpc.CreateTextRequest{GridID: root, X: 2, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdoptChildGrid(ctx, &rpc.AdoptChildGridRequest{
		TileID: text.ID, Version: text.Version, ChildGridID: "sshuuid/c1",
	}); !errors.Is(err, ErrNotWellTile) {
		t.Errorf("adopt onto a text tile: err = %v, want ErrNotWellTile", err)
	}

	// Version conflict: the optimistic claim is checked in-transaction.
	pw := createPluginWell(t, s, root, 4)
	if _, err := s.AdoptChildGrid(ctx, &rpc.AdoptChildGridRequest{
		TileID: pw.ID, Version: pw.Version + 7, ChildGridID: "sshuuid/c1",
	}); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale claim: err = %v, want ErrVersionConflict", err)
	}

	// Empty child grid.
	if _, err := s.AdoptChildGrid(ctx, &rpc.AdoptChildGridRequest{
		TileID: pw.ID, Version: pw.Version,
	}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("empty child grid: err = %v, want ErrInvalidArgument", err)
	}
}

// A clone of an unconfigured plugin well is another unconfigured plugin well
// (right-drag duplicates; the configure fact is user-visible state), and
// deleting one is an ordinary delete — childless means nothing cascades.
func TestPluginWell_CloneAndDelete(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile := createPluginWell(t, s, root, 0)

	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: tile.ID, Version: tile.Version, DestGridID: root, X: 5, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.ConfigurePluginID != "sshuuid" || clone.ChildGridID != "" {
		t.Errorf("clone = (configure %q, child %q), want an unconfigured plugin well",
			clone.ConfigurePluginID, clone.ChildGridID)
	}

	hardDelete(t, s, tile.ID)
	if _, err := s.GetTile(ctx, tile.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted plugin well still reads: %v", err)
	}
}
