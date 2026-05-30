package store

import (
	"context"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/rpc"
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
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
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
		Path: rpc.Path{}, GridID: root,
		X: 0, Y: 0, W: 1, H: 1, Data: []byte("# hi"),
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
		Path: rpc.Path{}, GridID: root,
		X: 0, Y: 0, W: 1, H: 1, URL: "https://example.com",
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "CreateURL", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

func TestEventCreateBlackHoleEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	ch, cancel := s.SubscribeEvents()
	defer cancel()

	if _, err := s.CreateBlackHole(ctx, &rpc.CreateBlackHoleRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "CreateBlackHole", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

func TestEventResizeTileEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.ResizeTile(ctx, &rpc.ResizeTileRequest{
		Path: rpc.Path{}, TileID: w.ID, Version: w.Version, W: 3, H: 3,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "ResizeTile", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

func TestEventSetWellViewEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.SetWellView(ctx, &rpc.SetWellViewRequest{
		Path: rpc.Path{}, TileID: w.ID, Version: w.Version,
		ViewX: 5, ViewY: 7, ViewZoom: 1.0,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "SetWellView", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

func TestEventSetRootViewEmitsGridChanged(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if err := s.SetRootView(ctx, &rpc.SetRootViewRequest{
		Cx: 1, Cy: 2, Zoom: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "SetRootView", got, map[rpc.EventKind]int{rpc.EventGridChanged: 1})
}

func TestEventDeleteTileEmitsTileRemoved(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: rpc.Path{}, TileID: w.ID, Version: w.Version,
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
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.MoveTile(ctx, &rpc.MoveTileRequest{
		Path: rpc.Path{}, TileID: w.ID, Version: w.Version,
		DestGridID: root, DestPath: rpc.Path{},
		X: 5, Y: 5,
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
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 5, Y: 5, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.MoveTile(ctx, &rpc.MoveTileRequest{
		Path: rpc.Path{}, TileID: target.ID, Version: target.Version,
		DestGridID: a.ChildGridID, DestPath: rpc.Path{WellIDs: []int64{a.ID}},
		X: 0, Y: 0,
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
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: w.ID, Version: w.Version,
		DestGridID: root, DestPath: rpc.Path{},
		X: 5, Y: 0,
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
		Path: rpc.Path{}, GridID: root,
		X: 0, Y: 0, W: 1, H: 1, Data: []byte("v1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.UpdateText(ctx, &rpc.UpdateTextRequest{
		Path: rpc.Path{}, TileID: f.ID, Version: f.Version, Data: []byte("v2"),
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "UpdateText", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

func TestEventCloneURLEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	s.SetURLDriver(NewFakeURLDriver())
	root := rootID(t, s)
	ctx := context.Background()
	src := createURLTileForTest(t, s, root, 0, "https://example.com")
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: src.ID, Version: src.Version,
		DestGridID: root, DestPath: rpc.Path{},
		X: 5, Y: 0,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "CloneURL", got, map[rpc.EventKind]int{rpc.EventTileChanged: 1})
}

// TestEventCowForkEmitsGridForked: a write into a clone-shared child grid
// causes the spine to fork. The mutation should emit one GridForked per
// forked grid plus the per-mutation TileChanged.
func TestEventCowForkEmitsGridForked(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []int64{w.ID}},
		GridID: w.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: w.ID, Version: w.Version,
		DestGridID: root, DestPath: rpc.Path{},
		X: 5, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.ResizeTile(ctx, &rpc.ResizeTileRequest{
		Path: rpc.Path{WellIDs: []int64{clone.ID}},
		TileID: inner.ID, Version: inner.Version, W: 2, H: 2,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "ResizeTile-with-fork", got, map[rpc.EventKind]int{
		rpc.EventGridForked:  1,
		rpc.EventTileChanged: 1,
	})
}
