package panepreview

import (
	"math"
	"math/rand"
	"testing"

	"github.com/josephburnett/gridwell/client/pane"
)

// TestPreviewIsScaledDescentTarget pins the continuity property. For any
// workspace tree the preview laid into the tile rect is the live layout under
// one scale and translate: every leaf's preview rect maps affinely onto its
// live rect, and its content cell size is the live cell size times the same
// factor. Descent grows the tile rect into the root rect and crosses no
// discontinuity.
func TestPreviewIsScaledDescentTarget(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	liveRoot := pane.Rect{X: 0, Y: 0, W: 1280, H: 800}

	for i := range 100 {
		tr := pane.NewTree()
		for range r.Intn(5) {
			ids := leafIDs(tr)
			if err := tr.SetFocus(ids[r.Intn(len(ids))]); err != nil {
				t.Fatal(err)
			}
			if _, err := tr.SplitOnSideAt(pane.Side(r.Intn(4)), 0.2+0.6*r.Float64()); err != nil {
				t.Fatal(err)
			}
		}

		tile := pane.Rect{X: 100 + float64(r.Intn(400)), Y: 50 + float64(r.Intn(300)),
			W: 40 + float64(r.Intn(300)), H: 40 + float64(r.Intn(300))}
		s := Scale(tile, liveRoot)
		if s <= 0 {
			t.Fatalf("case %d: scale = %v", i, s)
		}

		liveRects := pane.Layout(tr, liveRoot)
		for _, leaf := range Leaves(tr, tile, liveRoot) {
			live := liveRects[leaf.Pane.ID]
			// pane.Layout distributes ratios linearly, so each axis scales
			// independently and the live rect scaled per axis is the preview
			// rect.
			wantX := tile.X + (live.X-liveRoot.X)*(tile.W/liveRoot.W)
			wantY := tile.Y + (live.Y-liveRoot.Y)*(tile.H/liveRoot.H)
			wantW := live.W * (tile.W / liveRoot.W)
			wantH := live.H * (tile.H / liveRoot.H)
			if !close(leaf.Rect.X, wantX) || !close(leaf.Rect.Y, wantY) ||
				!close(leaf.Rect.W, wantW) || !close(leaf.Rect.H, wantH) {
				t.Fatalf("case %d leaf %s: preview rect %+v, want affine image %v,%v %vx%v",
					i, leaf.Pane.ID, leaf.Rect, wantX, wantY, wantW, wantH)
			}
			// A view is read at the size descent will show the leaf at.
			if leaf.Live != live {
				t.Fatalf("case %d leaf %s: live rect %+v, want %+v", i, leaf.Pane.ID, leaf.Live, live)
			}
			// Content continuity: the preview cell is the live cell times s.
			zoom := 0.25 * float64(r.Intn(8)+1)
			if !close(leaf.Cell(zoom), zoom*pane.CellPx*s) {
				t.Fatalf("case %d leaf %s: cell %v, want liveCell×s", i, leaf.Pane.ID, leaf.Cell(zoom))
			}
		}
	}
}

// TestZoomedLeafOwnsThePreview pins that a zoomed pane persists in the layout
// and fills the tile in the mini-render, as descent would restore it.
func TestZoomedLeafOwnsThePreview(t *testing.T) {
	tr := pane.NewTree()
	if _, err := tr.Split(pane.Vertical); err != nil {
		t.Fatal(err)
	}
	ids := leafIDs(tr)
	tr.ToggleZoom(ids[1])

	tile := pane.Rect{X: 10, Y: 10, W: 200, H: 100}
	leaves := Leaves(tr, tile, pane.Rect{W: 2000, H: 1000})
	if len(leaves) != 1 {
		t.Fatalf("zoomed preview has %d leaves, want 1", len(leaves))
	}
	if leaves[0].Pane.ID != ids[1] || leaves[0].Rect != tile {
		t.Fatalf("zoomed leaf %s rect %+v, want %s filling %+v", leaves[0].Pane.ID, leaves[0].Rect, ids[1], tile)
	}
}

func leafIDs(t *pane.Tree) []string {
	var ids []string
	t.Walk(func(p *pane.Pane) { ids = append(ids, p.ID) })
	return ids
}

func close(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
