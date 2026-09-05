package nav

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/scratch"
)

// The promote: an ephemeral url visit dragged onto a grid becomes a tile
// there, and the pane follows its content.

// visitPane is a pane sitting in the ephemeral visit v1, whose scratch grid is
// known to be sg.
func visitPane(id string) PaneView {
	p := contentPane(id, "v1")
	p.Scratch = scratch.Grid{Cached: true, ScratchGridID: "sg"}
	return p
}

func promoteGesture(created rpc.Tile) Gesture {
	return Gesture{Kind: GesturePromote, PaneID: "pane1", DestPaneID: "pane2",
		OldID: "v1", Created: created}
}

func TestPromote(t *testing.T) {
	created := rpc.Tile{ID: "n1", Kind: rpc.KindURL, GridID: "g2", X: 4, Y: 5, W: 1, H: 1}
	visit := &rpc.Tile{ID: "v1", Kind: rpc.KindURL, GridID: "sg"}

	t.Run("the visit freezes onto the new tile and the row dies", func(t *testing.T) {
		w := baseWorld(visitPane("pane1"), gridPane("pane2", "g2"))
		w.Promote = &PromoteWorld{OldTile: visit}
		plan := New().Do(promoteGesture(created), w)
		if !sameKinds(kinds(plan), []EffectKind{EffCloseStream, EffDeleteEphemeral,
			EffRelocatePane, EffScaleContent, EffPlaceURLView, EffRefreshOverlay,
			EffScheduleURLUpdate}) {
			t.Fatalf("effects = %v, want the promote", kinds(plan))
		}
		e := only(t, plan, EffCloseStream)
		if !e.Freeze || e.FreezeOnto == nil || e.FreezeOnto.TileID != "n1" {
			t.Fatalf("closed %+v, want the capture onto the tile it became", e)
		}
		if d := only(t, plan, EffDeleteEphemeral); d.TileID != "v1" || d.GridID != "sg" {
			t.Fatalf("deleted %+v, want the ephemeral row", d)
		}
		r := only(t, plan, EffRelocatePane)
		if r.DestPaneID != "pane2" || r.TileID != "n1" || r.Foot.X != 4 || r.Zoom <= 0 {
			t.Fatalf("relocated %+v, want the destination's place at the descent zoom", r)
		}
		if v := only(t, plan, EffPlaceURLView); v.Tile.ID != "n1" || v.PaneID != "pane1" {
			t.Fatalf("placed %+v, want the page live again on the new tile", v)
		}
	})

	t.Run("a split sibling still showing the visit keeps it", func(t *testing.T) {
		w := baseWorld(visitPane("pane1"), gridPane("pane2", "g2"), visitPane("pane3"))
		w.Promote = &PromoteWorld{OldTile: visit}
		plan := New().Do(promoteGesture(created), w)
		for _, k := range kinds(plan) {
			if k == EffDeleteEphemeral {
				t.Fatalf("deleted a row a sibling pane still shows: %v", kinds(plan))
			}
		}
	})

	t.Run("an unknown scratch grid keeps the row", func(t *testing.T) {
		// Not known yet is not a no: a delete is irreversible and is never
		// made on a guess.
		p := contentPane("pane1", "v1")
		p.Scratch = scratch.Grid{ScratchGridID: "sg"}
		w := baseWorld(p, gridPane("pane2", "g2"))
		w.Promote = &PromoteWorld{OldTile: visit}
		for _, k := range kinds(New().Do(promoteGesture(created), w)) {
			if k == EffDeleteEphemeral {
				t.Fatalf("deleted a row before its grid was known to be scratch")
			}
		}
	})

	t.Run("the origin pane moving on mid-create leaves the tile where it fell", func(t *testing.T) {
		w := baseWorld(contentPane("pane1", "other"), gridPane("pane2", "g2"))
		w.Promote = &PromoteWorld{OldTile: visit}
		if plan := New().Do(promoteGesture(created), w); len(plan.Effects) != 0 {
			t.Fatalf("promoted out of a pane that moved on: %v", kinds(plan))
		}
	})

	t.Run("a destination pane that closed plans nothing", func(t *testing.T) {
		w := baseWorld(visitPane("pane1"))
		w.Promote = &PromoteWorld{OldTile: visit}
		if plan := New().Do(promoteGesture(created), w); len(plan.Effects) != 0 {
			t.Fatalf("relocated onto a pane that is gone: %v", kinds(plan))
		}
	})
}
