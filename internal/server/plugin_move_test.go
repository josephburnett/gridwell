package server_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/events"
)

// Move is the parity source's move to another of its grids, answered only
// while it declares one: the entry's key there is the grid's name before it.
func (s *paritySource) Move(_ context.Context, req *pluginv1.MoveRequest) (*pluginv1.MoveResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.moves {
		return nil, status.Error(codes.Unimplemented, "method Move not implemented")
	}
	e, g := s.entry(req.Key)
	if e == nil || g != req.Grid {
		return nil, status.Errorf(codes.NotFound, "no %q in %q", req.Key, req.Grid)
	}
	s.grids[g] = slices.DeleteFunc(s.grids[g], func(x *pluginv1.Entry) bool { return x == e })
	e.Key = req.ToGrid + "/" + req.Key
	s.grids[req.ToGrid] = append(s.grids[req.ToGrid], e)
	s.bodies[e.Key] = s.bodies[req.Key]
	return &pluginv1.MoveResponse{Key: e.Key}, nil
}

func (f *parityFix) body(grid string) *gridwellv1.Tile {
	f.t.Helper()
	g, err := f.cl.GetGrid(f.ctx, grid)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, tl := range g.Tiles {
		if tl.AltText == "Body" {
			return tl
		}
	}
	return nil
}

// wantMoves fails unless grid carries the stamp the client's left-drag
// verdict reads (dragdrop.MoveForbidden).
func (f *parityFix) wantMoves(grid string, want bool) {
	f.t.Helper()
	g, err := f.cl.GetGrid(f.ctx, grid)
	if err != nil {
		f.t.Fatal(err)
	}
	if g.Grid.Moves == nil || g.Grid.GetMoves() != want {
		f.t.Fatalf("%s: moves = %v, want %v", grid, g.Grid.Moves, want)
	}
}

// A plugin that declares no move refuses a move to another of its grids with
// the reason, here and through a connection; one that declares it moves the
// entry, the row keeping what the node holds on it, and every open view of
// either grid is told. A declaration is a process's, so each is its own fixture.
func TestAPluginEntryMovesOnlyWhenThePluginMoves(t *testing.T) {
	f := newParityFix(t)
	f.wantMoves(f.away[onHome], true)
	f.wantMoves(f.away[onFar], true)
	for ns := range f.src {
		all, other := rpc.QualifyID(ns, rpc.EntryGridID("all")), rpc.QualifyID(ns, rpc.EntryGridID("other"))
		tl := f.body(all)
		_, err := f.cl.MoveTile(f.ctx, &gridwellv1.MoveTileRequest{TileId: tl.Id, GridId: other, X: 3, Y: 3, W: 1, H: 1, Claim: rpc.PlacementOf(tl)})
		if clientsync.Of(err) != clientsync.OutcomeRejected || !strings.Contains(err.Error(), "the plugin does not move entries between its grids") {
			t.Fatalf("%s: undeclared move = %v, want refused with the reason", ns, err)
		}
		f.wantMoves(all, false)
	}

	f = newParityFix(t, func(s *paritySource) { s.moves = true })
	for ns := range f.src {
		if _, err := f.cl.SetContentZoom(f.ctx, f.body(rpc.QualifyID(ns, rpc.EntryGridID("all"))).Id, 1.5); err != nil {
			t.Fatal(err)
		}
	}
	f.settle(onPlugin)
	f.settle(onFar)
	for len(f.events) > 0 {
		<-f.events
	}
	for ns := range f.src {
		all, other := rpc.QualifyID(ns, rpc.EntryGridID("all")), rpc.QualifyID(ns, rpc.EntryGridID("other"))
		f.wantMoves(all, true)
		tl := f.body(all)
		moved, err := f.cl.MoveTile(f.ctx, &gridwellv1.MoveTileRequest{TileId: tl.Id, GridId: other, X: 3, Y: 3, W: 1, H: 1, Claim: rpc.PlacementOf(tl)})
		if err != nil {
			t.Fatalf("%s: declared move: %v", ns, err)
		}
		if moved.GridId != other || moved.X != 3 || moved.Y != 3 || moved.ContentZoom != 1.5 {
			t.Fatalf("%s: moved to %s at %d,%d zoom %v; want %s at 3,3 keeping zoom 1.5", ns, moved.GridId, moved.X, moved.Y, moved.ContentZoom, other)
		}
		if f.body(all) != nil || f.body(other) == nil {
			t.Fatalf("%s: the entry is not on %s alone", ns, other)
		}
		pending := map[string]bool{all: true, other: true}
		for deadline := time.After(10 * time.Second); len(pending) > 0; {
			select {
			case ev := <-f.events:
				delete(pending, events.Route(ev).Fetch)
			case <-deadline:
				t.Fatalf("%s: no view was told of %v", ns, pending)
			}
		}
	}
}
