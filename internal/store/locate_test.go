package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// LocateTile re-derives a tile's path from its immutable id (issue #234):
// the containing-well chain outermost first, tracking moves.
func TestLocateTile(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	outer, err := s.CreateWell(ctx, &rpc.CreateWellRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateWell(ctx, &rpc.CreateWellRequest{GridID: outer.ChildGridID, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	text, err := s.CreateText(ctx, &rpc.CreateTextRequest{GridID: root, X: 3, Y: 0, W: 1, H: 1, Data: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}

	// At the root: an empty chain.
	wells, err := s.LocateTile(ctx, text.ID)
	if err != nil {
		t.Fatalf("locate at root: %v", err)
	}
	if len(wells) != 0 {
		t.Fatalf("root chain = %d wells, want none", len(wells))
	}

	// Move it two levels deep: the chain is [outer, inner].
	if _, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: text.ID, Version: text.Version,
		GridID: inner.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatalf("move into inner: %v", err)
	}
	wells, err = s.LocateTile(ctx, text.ID)
	if err != nil {
		t.Fatalf("locate after move: %v", err)
	}
	if len(wells) != 2 || wells[0].ID != outer.ID || wells[1].ID != inner.ID {
		ids := []string{}
		for _, w := range wells {
			ids = append(ids, w.ID)
		}
		t.Fatalf("chain = %v, want [outer=%s inner=%s]", ids, outer.ID, inner.ID)
	}
	if wells[0].GridID != root || wells[1].GridID != outer.ChildGridID {
		t.Fatalf("chain rows carry wrong grids: %+v", wells)
	}

	// A missing id is NotFound, never an empty chain.
	if _, err := s.LocateTile(ctx, "999999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing tile: %v, want ErrNotFound", err)
	}
}
