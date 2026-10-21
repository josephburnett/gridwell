package store

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// drainEvents collects every event published in the next short window
// after the subscribe is in place.
func drainEvents(t *testing.T, ch <-chan *gridwellv1.Event) []*gridwellv1.Event {
	t.Helper()
	var out []*gridwellv1.Event
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

// eventName names an event by the oneof arm it carries, so the tests can
// tally kinds. The names are the proto field names.
func eventName(ev *gridwellv1.Event) string {
	switch ev.Payload.(type) {
	case *gridwellv1.Event_GridChanged:
		return "grid_changed"
	case *gridwellv1.Event_GridFramingChanged:
		return "grid_framing_changed"
	case *gridwellv1.Event_TileChanged:
		return "tile_changed"
	case *gridwellv1.Event_TileRemoved:
		return "tile_removed"
	case *gridwellv1.Event_PluginHealth:
		return "plugin_health"
	}
	return ""
}

// countKinds tallies events by eventName.
func countKinds(evs []*gridwellv1.Event) map[string]int {
	m := map[string]int{}
	for _, ev := range evs {
		m[eventName(ev)]++
	}
	return m
}

// assertCounts fails if got != want.
func assertCounts(t *testing.T, label string, got, want map[string]int) {
	t.Helper()
	all := map[string]bool{}
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

func TestEventCreateDoorEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	ch, cancel := s.SubscribeEvents()
	defer cancel()

	if _, err := s.CreateDoor(ctx, root, 0, 0, 1, 1, "", false); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "CreateDoor", got, map[string]int{"tile_changed": 1})
}

func TestEventCreateTextEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	ch, cancel := s.SubscribeEvents()
	defer cancel()

	if _, err := s.CreateText(ctx, root, 0, 0, 1, 1, []byte("# hi")); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "CreateText", got, map[string]int{"tile_changed": 1})
}

func TestEventCreateURLEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	ch, cancel := s.SubscribeEvents()
	defer cancel()

	if _, err := s.CreateURL(ctx, root, 0, 0, 1, 1, "https://example.com", "", false); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "CreateURL", got, map[string]int{"tile_changed": 1})
}

func TestEventResizeTileEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateDoor(ctx, root, 0, 0, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
		TileId: w.Id,
		GridId: w.GridId, X: 0, Y: 0, W: 3, H: 3,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "ResizeTile", got, map[string]int{"tile_changed": 1})
}

func TestEventSetFramingEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateDoor(ctx, root, 0, 0, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		TileId: w.Id,
		Cx:     5, Cy: 7, Zoom: 1.0,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "SetFraming(tile)", got, map[string]int{"tile_changed": 1})
}

// A root's framing changes no listing, so it announces the three numbers it
// wrote and never GridChanged, which would cost every client a refetch.
func TestEventSetRootFramingEmitsGridFramingChanged(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	root, err := s.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		RootGridId: root, Cx: 1, Cy: 2, Zoom: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	evs := drainEvents(t, ch)
	assertCounts(t, "SetFraming(root)", countKinds(evs), map[string]int{"grid_framing_changed": 1})
	want := &gridwellv1.GridFramingChanged{GridId: root, ViewCx: 1, ViewCy: 2, ViewZoom: 0.5}
	if len(evs) == 1 && !proto.Equal(evs[0].GetGridFramingChanged(), want) {
		t.Errorf("SetFraming(root) announced %v, want %v", evs[0].GetGridFramingChanged(), want)
	}
}

func TestEventDeleteTileEmitsTileRemoved(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateDoor(ctx, root, 0, 0, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	// Park it in the trash first: this test pins the destruction event
	// shape, and TestDeleteToTrashEmitsMoveShape pins the trash-move one.
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{
		TileId: w.Id,
	}); err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{
		TileId: w.Id,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "DeleteTile", got, map[string]int{"tile_removed": 1})
}

func TestEventMoveTileWithinGridEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateDoor(ctx, root, 0, 0, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
		TileId: w.Id,
		GridId: root, X: 5, Y: 5, W: w.W, H: w.H,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "MoveTile-same-grid", got, map[string]int{"tile_changed": 1})
}

func TestEventMoveTileAcrossGridsEmitsRemovedAndChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	a, err := s.CreateDoor(ctx, root, 0, 0, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.CreateDoor(ctx, root, 5, 5, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
		TileId: target.Id,
		GridId: a.ChildGridId, X: 0, Y: 0, W: target.W, H: target.H,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "MoveTile-cross-grid", got, map[string]int{
		"tile_removed": 1,
		"tile_changed": 1,
	})
}

func TestEventCopyTileEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateDoor(ctx, root, 0, 0, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     w.Id,
		DestGridId: root, X: 5, Y: 0,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "CopyTile", got, map[string]int{"tile_changed": 1})
}

func TestEventUpdateTextEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	f, err := s.CreateText(ctx, root, 0, 0, 1, 1, []byte("v1"))
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.WriteContent(ctx, f.Id, f.Version, []byte("v2")); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "UpdateText", got, map[string]int{"tile_changed": 1})
}

func TestEventCopyURLEmitsTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	src := createURLTileForTest(t, s, root, 0, "https://example.com")
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     src.Id,
		DestGridId: root, X: 5, Y: 0,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "CopyURL", got, map[string]int{"tile_changed": 1})
}

// TestEventCopyEditEmitsOnlyTileChanged: a copy is an independent copy, so
// editing inside it is a plain in-place mutation — just a TileChanged, no fork
// machinery (there is no fork under eager copy).
func TestEventCopyEditEmitsOnlyTileChanged(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateDoor(ctx, root, 0, 0, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDoor(ctx, w.ChildGridId, 0, 0, 1, 1, "", false); err != nil {
		t.Fatal(err)
	}
	cp, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     w.Id,
		DestGridId: root, X: 5, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	copyChild, err := s.GetGrid(ctx, cp.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	cInner := copyChild.Tiles[0]

	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
		TileId: cInner.Id,
		GridId: cInner.GridId, X: 0, Y: 0, W: 2, H: 2,
	}); err != nil {
		t.Fatal(err)
	}
	got := countKinds(drainEvents(t, ch))
	assertCounts(t, "ResizeTile-in-copy", got, map[string]int{
		"tile_changed": 1,
	})
}
