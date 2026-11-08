package server

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// A directory's selection is the node's fact, on the node's row for that
// grid, as its framing is. Selecting a file touches it, so it gets its row;
// the set reads back in the grid's own spelling of each tile; and a file the
// directory stops listing leaves the set.
func TestADirectorysSelectionIsTheNodesFact(t *testing.T) {
	f := newFilesServer(t)
	cl, st, fsRoot := f.cl, f.db, f.root
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	grid := filesGrid(t, f, 3)
	g, err := cl.GetGrid(ctx, grid)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, tl := range g.Tiles {
		ids[tl.AltText] = tl.Id
	}
	f0, f1 := ids["f0.txt"], ids["f1.txt"]
	if f0 == "" || f1 == "" {
		t.Fatalf("listing names %v", ids)
	}
	tiles0, _ := filesRows(t, st)

	told := make(chan []string, 8)
	stream, err := cl.Subscribe(ctx)
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

	if err := cl.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: grid, Add: []string{f1, f0}}); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	want := slices.Sorted(slices.Values([]string{f0, f1}))
	select {
	case got := <-told:
		if !slices.Equal(slices.Sorted(slices.Values(got)), want) {
			t.Errorf("SelectionChanged carries %v, want %v", got, want)
		}
	case <-ctx.Done():
		t.Fatal("no SelectionChanged for the directory")
	}
	if tiles, _ := filesRows(t, st); tiles != tiles0+2 {
		t.Errorf("selecting two files minted %d rows, want 2", tiles-tiles0)
	}
	g, err = cl.GetGrid(ctx, grid)
	if err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(slices.Values(g.Grid.Selection)); !slices.Equal(got, want) {
		t.Errorf("the grid reads back %v, want %v", g.Grid.Selection, want)
	}
	if err := cl.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: grid, Add: []string{f0}}); err != nil {
		t.Errorf("a select landing where the row is: %v, want nil", err)
	}

	if err := os.Remove(filepath.Join(fsRoot, "f0.txt")); err != nil {
		t.Fatal(err)
	}
	g, err = cl.GetGrid(ctx, grid)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.Grid.Selection, []string{f1}) {
		t.Errorf("after the directory stopped listing f0 the grid reads %v, want [%s]", g.Grid.Selection, f1)
	}
}
