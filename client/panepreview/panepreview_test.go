package panepreview

import (
	"math"
	"math/rand"
	"testing"

	"github.com/josephburnett/gridwell/client/pane"
)

// TestPreviewIsScaledDescentTarget is the continuity property — the pane-tile
// face of "preview = descent target": for any workspace tree, the preview
// laid into the tile rect is EXACTLY the live layout under one uniform
// scale-and-translate. Every leaf's preview rect maps affinely onto its live
// rect, and its content cell size is the live cell size times the same
// factor — so descent (the tile rect growing into the root rect) crosses no
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
		tr.Walk(func(p *pane.Pane) { p.Zoom = 0.25 * float64(r.Intn(8)+1) })

		tile := pane.Rect{X: 100 + float64(r.Intn(400)), Y: 50 + float64(r.Intn(300)),
			W: 40 + float64(r.Intn(300)), H: 40 + float64(r.Intn(300))}
		s := Scale(tile, liveRoot)
		if s <= 0 {
			t.Fatalf("case %d: scale = %v", i, s)
		}

		liveRects := pane.Layout(tr, liveRoot)
		for _, leaf := range Leaves(tr, tile, s) {
			live := liveRects[leaf.Pane.ID]
			// The live rect scaled by (tile/liveRoot) per axis gives the
			// preview rect: Layout distributes ratios linearly, so each axis
			// scales independently by tileW/liveW (and tileH/liveH).
			wantX := tile.X + (live.X-liveRoot.X)*(tile.W/liveRoot.W)
			wantY := tile.Y + (live.Y-liveRoot.Y)*(tile.H/liveRoot.H)
			wantW := live.W * (tile.W / liveRoot.W)
			wantH := live.H * (tile.H / liveRoot.H)
			if !close(leaf.Rect.X, wantX) || !close(leaf.Rect.Y, wantY) ||
				!close(leaf.Rect.W, wantW) || !close(leaf.Rect.H, wantH) {
				t.Fatalf("case %d leaf %s: preview rect %+v, want affine image %v,%v %vx%v",
					i, leaf.Pane.ID, leaf.Rect, wantX, wantY, wantW, wantH)
			}
			// Content continuity: previewCell = liveCell × s.
			if !close(leaf.PreviewCell, leaf.Pane.Zoom*pane.CellPx*s) {
				t.Fatalf("case %d leaf %s: previewCell %v, want liveCell×s", i, leaf.Pane.ID, leaf.PreviewCell)
			}
		}
	}
}

// TestZoomedLeafOwnsThePreview: tmux-style pane zoom persists in the layout;
// the mini-render must show the zoomed pane filling the tile, exactly as
// descent would restore it.
func TestZoomedLeafOwnsThePreview(t *testing.T) {
	tr := pane.NewTree()
	if _, err := tr.Split(pane.Vertical); err != nil {
		t.Fatal(err)
	}
	ids := leafIDs(tr)
	tr.ToggleZoom(ids[1])

	tile := pane.Rect{X: 10, Y: 10, W: 200, H: 100}
	leaves := Leaves(tr, tile, 0.1)
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
