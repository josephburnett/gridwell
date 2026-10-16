package server_test

import (
	"context"
	"slices"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The selection through the whole seam, at home and on a far node alike: the
// client's door, the router, the transport, the far node's door and store. A
// write claims the set it saw, lands on the grid's row there, comes back to
// the writer as SelectionChanged in the writer's own spelling of every id,
// reads back on the grid, moves no version, and a stale claim is refused with
// the conflict the client reconciles from.
func TestTheSelectionIsTheOwningNodesFact(t *testing.T) {
	m := staleMesh(t)
	me := m.nodes[meNode]
	x := int64(60)
	for where, grid := range staleWhere(m) {
		t.Run(where, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			x += 3
			a := m.text(ctx, meNode, grid, x, "a")
			b := m.text(ctx, meNode, grid, x+1, "b")
			before, err := me.cl.GetGrid(ctx, grid)
			if err != nil {
				t.Fatal(err)
			}
			told := make(chan []string, 8)
			stream, err := me.cl.Subscribe(ctx)
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				defer stream.Close()
				for {
					ev, ok, err := stream.Recv()
					if err != nil || !ok {
						return
					}
					if sc := ev.GetSelectionChanged(); sc != nil && sc.GridId == grid {
						told <- sc.TileIds
					}
				}
			}()
			awaitSet := func(what string, want ...string) {
				t.Helper()
				slices.Sort(want)
				for {
					select {
					case got := <-told:
						got = slices.Sorted(slices.Values(got))
						if slices.Equal(got, want) {
							return
						}
					case <-ctx.Done():
						t.Fatalf("%s: no SelectionChanged carrying %v", what, want)
					}
				}
			}

			if err := me.cl.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: grid,
				Claim: &gridwellv1.SelectionClaim{}, TileIds: []string{a.Id, b.Id}}); err != nil {
				t.Fatalf("SetSelection: %v", err)
			}
			awaitSet("select", a.Id, b.Id)
			after, err := me.cl.GetGrid(ctx, grid)
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Sorted(slices.Values(after.Grid.Selection)); !slices.Equal(got, slices.Sorted(slices.Values([]string{a.Id, b.Id}))) {
				t.Errorf("the grid reads back selection %v, want [%s %s]", after.Grid.Selection, a.Id, b.Id)
			}
			if after.Grid.Version != before.Grid.Version {
				t.Errorf("grid version %d -> %d under a selection", before.Grid.Version, after.Grid.Version)
			}
			for _, tl := range after.Tiles {
				if (tl.Id == a.Id && tl.Version != a.Version) || (tl.Id == b.Id && tl.Version != b.Version) {
					t.Errorf("tile %s's version moved under a selection", tl.Id)
				}
			}

			// Another window still holding the empty set.
			wantConflict(t, "a stale selection", me.cl.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: grid,
				Claim: &gridwellv1.SelectionClaim{}, TileIds: []string{a.Id}}))

			// A tile that leaves the grid leaves the set, told with the rest.
			if _, err := me.cl.DeleteTile(ctx, rpc.DeleteOf(a)); err != nil {
				t.Fatal(err)
			}
			awaitSet("delete", b.Id)
			if err := me.cl.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: grid,
				Claim: &gridwellv1.SelectionClaim{TileIds: []string{b.Id}}}); err != nil {
				t.Fatalf("clearing: %v", err)
			}
			awaitSet("clear")
		})
	}
}
