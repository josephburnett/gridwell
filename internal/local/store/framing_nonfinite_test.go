package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A framing that is not a view is refused on both arms with the reason, and
// the stored framing stays as it was. SQLite binds NaN as NULL, so without
// the refusal the root arm stores a center that reads back as the origin and
// the tile arm fails with an internal constraint error (the 2026-10-04 trace).
func TestSetFramingRefusesNonFinite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	w, err := s.CreateWell(ctx, root, 0, 0, 9, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	good := mkFraming(-32.5, -122.6, 0.02)
	if _, err := s.SetFraming(ctx, &gridwellv1.SetFramingRequest{RootGridId: root, Cx: good.Cx(), Cy: good.Cy(), Zoom: good.Zoom()}); err != nil {
		t.Fatal(err)
	}
	well := mkFraming(0.43, 3.7, 0.65)
	if _, err := s.SetFraming(ctx, &gridwellv1.SetFramingRequest{TileId: w.Id, Cx: well.Cx(), Cy: well.Cy(), Zoom: well.Zoom()}); err != nil {
		t.Fatal(err)
	}

	nan, inf := math.NaN(), math.Inf(1)
	for _, tc := range []struct {
		name string
		req  *gridwellv1.SetFramingRequest
	}{
		{"root NaN center", &gridwellv1.SetFramingRequest{RootGridId: root, Cx: nan, Cy: nan, Zoom: 0.0045}},
		{"root Inf zoom", &gridwellv1.SetFramingRequest{RootGridId: root, Cx: 1, Cy: 1, Zoom: inf}},
		{"root zero zoom", &gridwellv1.SetFramingRequest{RootGridId: root, Cx: 1, Cy: 1}},
		{"tile NaN center", &gridwellv1.SetFramingRequest{TileId: w.Id, Cx: nan, Cy: nan, Zoom: 0.05}},
		{"tile Inf center", &gridwellv1.SetFramingRequest{TileId: w.Id, Cx: inf, Cy: 1, Zoom: 0.05}},
		{"tile zero zoom", &gridwellv1.SetFramingRequest{TileId: w.Id, Cx: 4.5, Cy: 3.5}},
		{"tile negative zoom", &gridwellv1.SetFramingRequest{TileId: w.Id, Cx: 4.5, Cy: 3.5, Zoom: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.SetFraming(ctx, tc.req)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("SetFraming = %v, want ErrInvalidArgument", err)
			}
		})
	}

	v, err := s.RootFraming(ctx)
	if err != nil || v != rpc.Saved(good) {
		t.Errorf("root framing after the refused writes = %+v err=%v, want %+v", v, err, good)
	}
	tile, err := s.GetTile(ctx, w.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := rpc.ViewOf(tile.ViewCx, tile.ViewCy, tile.ViewZoom); got != rpc.Saved(well) {
		t.Errorf("well framing after the refused writes = %+v, want %+v", got, well)
	}
}

// Never visited is NULL in storage on every path that makes a doorway — a
// fresh well, an exit well with no view, a clone of either — and reads back
// as an rpc.View without a framing; nothing writes a zero for it.
func TestNeverVisitedRoundTripsAsNone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	if v, err := s.GridFraming(root); err != nil || v != (rpc.View{}) {
		t.Errorf("a fresh root's framing = %+v err=%v, want none", v, err)
	}
	fresh, err := s.CreateWell(ctx, root, 0, 0, 2, 2, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	exit, err := s.CreateExitWell(ctx, root, 3, 0, 1, 1, "aabbccddaabbccddaabbccddaabbccdd/7", "exit", rpc.View{})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{fresh.Id, exit.Id}
	for i, src := range []string{fresh.Id, exit.Id} {
		c, err := s.CloneTile(ctx, &gridwellv1.CloneTileRequest{TileId: src, DestGridId: root, X: int64(10 + 3*i), Y: 10})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.Id)
	}
	for _, id := range ids {
		tile, err := s.GetTile(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if v := rpc.ViewOf(tile.ViewCx, tile.ViewCy, tile.ViewZoom); v != (rpc.View{}) {
			t.Errorf("tile %s (%s) reads a framing %+v, want none", id, tile.AltText, v)
		}
		lid, err := parseID(id)
		if err != nil {
			t.Fatal(err)
		}
		var cx, cy, zoom sql.NullFloat64
		if err := s.db.QueryRowContext(ctx, `SELECT view_cx, view_cy, view_zoom FROM tiles WHERE id = ?`, lid).
			Scan(&cx, &cy, &zoom); err != nil {
			t.Fatal(err)
		}
		if cx.Valid || cy.Valid || zoom.Valid {
			t.Errorf("tile %s (%s) stores (%+v, %+v, %+v), want three NULLs", id, tile.AltText, cx, cy, zoom)
		}
	}
}
