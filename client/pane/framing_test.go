package pane

import (
	"reflect"
	"testing"
)

// The framing writeback has ONE question: which row owns the framing of the
// place this pane is at? The frame stack answers it — the doorway you came
// in by, or the grid itself when you came in by nothing.
func TestFramingTargetPicksTheDoorway(t *testing.T) {
	// A root grid: no doorway, the grid row owns it.
	s := NewStack("home/1")
	if got := s.FramingTarget(); got.TileID != "" || got.RootGridID != "home/1" || got.Content {
		t.Fatalf("root = %+v", got)
	}

	// A well descent: the well is the doorway, and it lives one level out.
	s.Push(Frame{Door: "4"})
	s.Push(Frame{Door: "9"})
	got := s.FramingTarget()
	if got.TileID != "9" || got.DoorAnchor != "home/1" ||
		!reflect.DeepEqual(got.DoorPath, []string{"4"}) || got.RootGridID != "home/1" {
		t.Fatalf("well = %+v", got)
	}

	// A namespace crossing: the link tile is the doorway, and it lives in
	// the level below. The frame remembers it, so nothing has to search the
	// parent grid for a well whose child matches the anchor.
	s.Push(Frame{GridID: "k3x9m2q/1", Door: "lnk"})
	got = s.FramingTarget()
	if got.TileID != "lnk" || got.DoorAnchor != "home/1" ||
		!reflect.DeepEqual(got.DoorPath, []string{"4", "9"}) {
		t.Fatalf("portal = %+v", got)
	}
	// The fallback for a doorway with no row (a + menu portal has no tile in
	// the origin grid) is the level's own root grid.
	if got.RootGridID != "k3x9m2q/1" {
		t.Fatalf("portal fallback root = %q", got.RootGridID)
	}

	// A content descent settles its text scroll, not grid framing.
	s.Push(Frame{Door: "77", Content: true})
	if got := s.FramingTarget(); !got.Content || got.TileID != "77" {
		t.Fatalf("content = %+v", got)
	}
}

// The one-active-surface rule for grid framing: among panes showing the same
// grid, only the focused one writes; a sole viewer always writes.
func TestFramingWriters(t *testing.T) {
	panes := []PaneGrid{
		{PaneID: "a", GridID: "g1"},
		{PaneID: "b", GridID: "g1"}, // shares g1 with a
		{PaneID: "c", GridID: "g2"}, // sole viewer
	}
	w := FramingWriters(panes, "a")
	if !w["a"] || w["b"] || !w["c"] {
		t.Errorf("focused=a: got %+v, want a and c writing, b passive", w)
	}
	w = FramingWriters(panes, "c")
	if w["a"] || w["b"] || !w["c"] {
		t.Errorf("focused=c: got %+v, want only c writing (g1 has no active surface)", w)
	}
	if w := FramingWriters(nil, "x"); len(w) != 0 {
		t.Errorf("no panes: got %+v", w)
	}
}

// One live surface per content tile: the opener takes over and every other
// holder freezes, at any stack level. A pane already holding the tile is not
// asked to close, so a keep-alive return is idempotent.
func TestTakeOverFreezesEveryOtherHolder(t *testing.T) {
	holders := []Holder{
		{PaneID: "p1", TileID: "u/7"},
		{PaneID: "w1:p1", TileID: "u/7"},
		{PaneID: "p2", TileID: "u/9"},
	}
	if got := TakeOver(holders, "p2", "u/7"); !reflect.DeepEqual(got, []string{"p1", "w1:p1"}) {
		t.Fatalf("takeover = %v", got)
	}
	if got := TakeOver(holders, "p1", "u/7"); !reflect.DeepEqual(got, []string{"w1:p1"}) {
		t.Fatalf("opener must not close itself: %v", got)
	}
	if got := TakeOver(holders, "p9", "u/44"); got != nil {
		t.Fatalf("nobody holds it: %v", got)
	}
}
