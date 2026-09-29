package pane

import (
	"reflect"
	"testing"
)

// Which row owns the framing of the place this pane is at: the doorway you
// came in by, or the grid itself when you came in by nothing.
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

	// The link tile is the doorway and lives in the level below. The frame
	// remembers it, so nothing searches the parent grid for it.
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

// One live surface per content tile: the opener takes the surface another pane
// holds, at any stack level, and a pane that already holds it keeps it.
func TestTakeOver(t *testing.T) {
	cases := []struct {
		name    string
		holders []Holder
		opener  string
		tile    string
		want    Engagement
	}{
		{"nobody holds it: place a fresh surface",
			[]Holder{{"p2", "u/9"}}, "p1", "u/7", Engagement{}},
		{"another pane holds it: move it here",
			[]Holder{{"p1", "u/7"}, {"p2", "u/9"}}, "w1:p1", "u/7", Engagement{From: "p1"}},
		{"a parked outer level holds it: the move crosses levels",
			[]Holder{{"w1:p3", "u/7"}}, "p1", "u/7", Engagement{From: "w1:p3"}},
		{"the opener holds it: keep",
			[]Holder{{"p1", "u/7"}}, "p1", "u/7", Engagement{Keep: true}},
		{"the opener holds another tile: not this tile's surface",
			[]Holder{{"p1", "u/9"}}, "p1", "u/7", Engagement{}},
		{"a broken rule: one moves, the rest close",
			[]Holder{{"p1", "u/7"}, {"p2", "u/7"}}, "p3", "u/7", Engagement{From: "p1", Close: []string{"p2"}}},
		{"a broken rule while keeping: every other closes",
			[]Holder{{"p1", "u/7"}, {"p2", "u/7"}}, "p2", "u/7", Engagement{Keep: true, Close: []string{"p1"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TakeOver(c.holders, c.opener, c.tile); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("TakeOver = %+v, want %+v", got, c.want)
			}
		})
	}
}

// A surface that cannot move closes every other holder, the one that would
// have moved included.
func TestEngagementOthers(t *testing.T) {
	e := TakeOver([]Holder{{"p1", "u/7"}, {"w1:p1", "u/7"}, {"p2", "u/9"}}, "p2", "u/7")
	if got := e.Others(); !reflect.DeepEqual(got, []string{"p1", "w1:p1"}) {
		t.Fatalf("others = %v", got)
	}
	if got := TakeOver(nil, "p9", "u/44").Others(); got != nil {
		t.Fatalf("nobody holds it: %v", got)
	}
}

// Leaving a level hands a surface back to the returning pane that shows its
// tile, and closes it only when the tile leaves every pane.
func TestHeir(t *testing.T) {
	returning := []Holder{{"p1", "u/9"}, {"p2", "u/7"}, {"p3", "u/7"}}
	cases := []struct {
		name    string
		tile    string
		holders []Holder
		want    string
	}{
		{"the first returning pane showing the tile", "u/7", nil, "p2"},
		{"one already holding a surface is passed over", "u/7", []Holder{{"p2", "u/8"}}, "p3"},
		{"no returning pane shows it: close", "u/44", nil, ""},
		{"every one showing it holds a surface: close", "u/7", []Holder{{"p2", "u/7"}, {"p3", "u/1"}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Heir(c.tile, returning, c.holders); got != c.want {
				t.Fatalf("Heir = %q, want %q", got, c.want)
			}
		})
	}
}
