package store

import (
	"context"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
)

// drainEvents collects every event published in the next short window
// after the subscribe is in place.
func drainEvents(t *testing.T, ch <-chan rpc.Event) []rpc.Event {
	t.Helper()
	var out []rpc.Event
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-time.After(50 * time.Millisecond):
			return out
		}
	}
}

// countKinds tallies events by their EventKind.
func countKinds(evs []rpc.Event) map[rpc.EventKind]int {
	m := map[rpc.EventKind]int{}
	for _, ev := range evs {
		m[ev.Kind]++
	}
	return m
}

// assertCounts fails if got != want.
func assertCounts(t *testing.T, label string, got, want map[rpc.EventKind]int) {
	t.Helper()
	all := map[rpc.EventKind]bool{}
	for k := range got {
		all[k] = true
	}
	for k := range want {
		all[k] = true
	}
	for k := range all {
		if got[k] != want[k] {
			t.Errorf("%s: kind=%s got=%d want=%d (all: got=%v want=%v)",
				label, k, got[k], want[k], got, want)
		}
	}
}

func TestEventCreateWellEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	ch, cancel := s.SubscribeEvents()
	defer cancel()

	if _, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "CreateWell", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

func TestEventCreateTextEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	ch, cancel := s.SubscribeEvents()
	defer cancel()

	if _, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root,
		X:      0, Y: 0, W: 1, H: 1, Data: []byte("# hi"),
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "CreateText", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

func TestEventCreateURLEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	ch, cancel := s.SubscribeEvents()
	defer cancel()

	if _, err := s.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: root,
		X:      0, Y: 0, W: 1, H: 1, URL: "https://example.com",
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "CreateURL", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

func TestEventResizeTileEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: w.ID, Version: w.Version,
		GridID: w.GridID, X: 0, Y: 0, W: 3, H: 3,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "ResizeTile", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

func TestEventSetFramingEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.SetFraming(ctx, &rpc.SetFramingRequest{
		TileID: w.ID, Version: w.Version,
		Framing: rpc.Framing{Cx: 5, Cy: 7, Zoom: 1.0},
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "SetFraming(tile)", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

func TestEventSetRootFramingEmitsGridChanged(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	root, err := s.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFraming(ctx, &rpc.SetFramingRequest{
		RootGridID: root, Framing: rpc.Framing{Cx: 1, Cy: 2, Zoom: 0.5},
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "SetFraming(root)", got, map[rpc.EventKind]int{rpc.EventGridChanged: 1})
}

func TestEventDeleteTileEmitsTileRemoved(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Park it in the trash first (#262): this test pins the DESTRUCTION
	// event shape; the trash-move shape is TestDeleteToTrashEmitsMoveShape.
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		TileID: w.ID, Version: w.Version,
	}); err != nil {
		t.Fatal(err)
	}
	cur, err := s.GetTile(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		TileID: w.ID, Version: cur.Version,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "DeleteTile", got, map[rpc.EventKind]int{rpc.EventTileRemoved: 1})
}

func TestEventMoveTileWithinGridEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: w.ID, Version: w.Version,
		GridID: root, X: 5, Y: 5, W: w.W, H: w.H,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "MoveTile-same-grid", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

func TestEventMoveTileAcrossGridsEmitsRemovedAndChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	a, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 5, Y: 5, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: target.ID, Version: target.Version,
		GridID: a.ChildGridID, X: 0, Y: 0, W: target.W, H: target.H,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "MoveTile-cross-grid", got, map[rpc.EventKind]int{
		rpc.EventTileRemoved: 1,
		rpc.EventTileChanged: 1,
	})
}

func TestEventCloneTileEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: w.ID, Version: w.Version,
		DestGridID: root, X: 5, Y: 0,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "CloneTile", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

func TestEventUpdateTextEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	f, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root,
		X:      0, Y: 0, W: 1, H: 1, Data: []byte("v1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.WriteContent(ctx, f.ID, f.Version, []byte("v2")); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "UpdateText", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

func TestEventCloneURLEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	src := createURLTileForTest(t, s, root, 0, "https://example.com")
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: src.ID, Version: src.Version,
		DestGridID: root, X: 5, Y: 0,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "CloneURL", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

// TestEventCloneEditEmitsOnlyTileChanged: a clone is an independent copy, so
// editing inside it is a plain in-place mutation — just a TileChanged, no fork
// machinery (there is no fork under copy-on-clone).
func TestEventCloneEditEmitsOnlyTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: w.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: w.ID, Version: w.Version,
		DestGridID: root, X: 5, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	cloneChild, err := s.GetGrid(ctx, clone.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	cInner := cloneChild.Tiles[0]

	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: cInner.ID, Version: cInner.Version,
		GridID: cInner.GridID, X: 0, Y: 0, W: 2, H: 2,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "ResizeTile-in-clone", got, map[rpc.EventKind]int{
		rpc.EventTileChanged: 1,
	})
}
