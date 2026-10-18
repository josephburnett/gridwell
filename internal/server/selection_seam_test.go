package server_test

import (
	"context"
	"slices"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
)

// The selection through the whole seam, at home and on a far node alike: the
// client's API, the router, the transport, the far node's API and store. A
// select names what it adds and removes and claims nothing: it lands on the
// grid's row there, comes back to the writer as the whole set in
// SelectionChanged in the writer's own spelling of every id, reads back on the
// grid, moves no version, and two writers' selects of different tiles merge.
// The client's cache, folding the real stream, holds each set the node told.
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
			// The client's cache, folding the stream as the client does.
			c := cache.New()
			c.PutGrid(before.Grid, before.Tiles)
			told := make(chan *gridwellv1.Event, 8)
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
						told <- ev
					}
				}
			}()
			awaitSet := func(what string, want ...string) {
				t.Helper()
				slices.Sort(want)
				for {
					select {
					case ev := <-told:
						c.Apply(ev)
						held, _ := c.Grid(grid)
						if slices.Equal(slices.Sorted(slices.Values(held.Meta.Selection)), want) {
							return
						}
					case <-ctx.Done():
						t.Fatalf("%s: the client's cache never held %v", what, want)
					}
				}
			}

			if err := me.cl.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: grid, Add: []string{a.Id}}); err != nil {
				t.Fatalf("SetSelection: %v", err)
			}
			awaitSet("select", a.Id)
			// Another window, which never saw a selected, adds b: both stand.
			if err := me.cl.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: grid, Add: []string{b.Id}}); err != nil {
				t.Fatalf("a second writer's select: %v", err)
			}
			awaitSet("a second writer's select", a.Id, b.Id)
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

			// A tile that leaves the grid leaves the set, told with the rest.
			if _, err := me.cl.DeleteTile(ctx, rpc.DeleteOf(a)); err != nil {
				t.Fatal(err)
			}
			awaitSet("delete", b.Id)
			// A recursive clear crosses every hop with both its flags.
			if err := me.cl.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: grid, Clear: true, Recurse: true}); err != nil {
				t.Fatalf("clearing: %v", err)
			}
			awaitSet("clear")
		})
	}
}

// The all-clear empties every set the node holds and every live far node's,
// through the real transport, and a far node asked over its connection API
// clears its own rows and forwards nothing, so two nodes connected both ways
// do not echo it back and forth.
func TestTheAllClearReachesEveryFarNodeOnce(t *testing.T) {
	m := newMesh(t, []string{meNode, aNode}, []meshEdge{{meNode, "toa", aNode}, {aNode, "tome", meNode}})
	me := m.nodes[meNode]
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	home, far := m.home(meNode, meNode), m.home(meNode, aNode, "toa")
	for i, grid := range []string{home, far} {
		tl := m.text(ctx, meNode, grid, int64(70+i), "x")
		if err := me.cl.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: grid, Add: []string{tl.Id}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []*gridwellv1.SetSelectionRequest{{All: true}, {GridId: home, Clear: true, All: true}} {
		if err := me.cl.SetSelection(ctx, bad); err == nil {
			t.Errorf("%v was not refused", bad)
		}
	}
	if err := me.cl.SetSelection(ctx, &gridwellv1.SetSelectionRequest{Clear: true, All: true}); err != nil {
		t.Fatalf("the all-clear: %v", err)
	}
	for _, grid := range []string{home, far} {
		g, err := me.cl.GetGrid(ctx, grid)
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Grid.Selection) != 0 {
			t.Errorf("grid %s kept %v", grid, g.Grid.Selection)
		}
	}
}
