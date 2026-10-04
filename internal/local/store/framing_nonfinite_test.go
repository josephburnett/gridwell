package store

import (
	"context"
	"errors"
	"math"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// A non-finite framing is no framing: SQLite binds NaN as NULL, so the root
// arm (nullable root_* columns) stores a NULL center that reads back as the
// origin, and the tile arm (NOT NULL view_*) fails with an internal
// constraint error the client shows verbatim. Both arms must refuse it with
// the reason, and leave the stored framing as it was. Red on purpose
// (2026-10-04 trace: SetFraming/root ok on "center NaN,NaN", tile 8
// "NOT NULL constraint failed: tiles.view_cx").
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
	if _, err := s.SetFraming(ctx, &gridwellv1.SetFramingRequest{TileId: w.Id, Cx: 0.43, Cy: 3.7, Zoom: 0.65}); err != nil {
		t.Fatal(err)
	}

	nan, inf := math.NaN(), math.Inf(1)
	for _, tc := range []struct {
		name string
		req  *gridwellv1.SetFramingRequest
	}{
		{"root NaN center", &gridwellv1.SetFramingRequest{RootGridId: root, Cx: nan, Cy: nan, Zoom: 0.0045}},
		{"root Inf zoom", &gridwellv1.SetFramingRequest{RootGridId: root, Cx: 1, Cy: 1, Zoom: inf}},
		{"tile NaN center", &gridwellv1.SetFramingRequest{TileId: w.Id, Cx: nan, Cy: nan, Zoom: 0.05}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.SetFraming(ctx, tc.req)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("SetFraming = %v, want ErrInvalidArgument", err)
			}
		})
	}

	v, err := s.RootFraming(ctx)
	got, ok := v.Framing()
	if err != nil || !ok || got != good {
		t.Errorf("root framing after the refused writes = %+v ok=%v err=%v, want %+v", got, ok, err, good)
	}
}
