package menu

// The templates: the tiles on home's templates grid, drawn in the strip
// before the swatches, after the copy swatch.

import (
	"cmp"
	"slices"
	"strconv"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Templates is the grid's tiles newest first. A row's times are not on the
// wire, and home mints row ids in creation order, so the id is the order.
func Templates(tiles map[string]*gridwellv1.Tile) []*gridwellv1.Tile {
	out := make([]*gridwellv1.Tile, 0, len(tiles))
	for _, t := range tiles {
		out = append(out, t)
	}
	minted := func(t *gridwellv1.Tile) int64 {
		n, _ := strconv.ParseInt(rpc.LocalOf(t.Id), 10, 64)
		return n
	}
	slices.SortFunc(out, func(a, b *gridwellv1.Tile) int {
		return cmp.Or(cmp.Compare(minted(b), minted(a)), cmp.Compare(a.Id, b.Id))
	})
	return out
}

// Fit is where a w×h tile sits in a strip slot: the cell size it is drawn
// at and its rect, its shape kept and centered. The strip's draw and the
// ghost a press on it starts both read it.
func Fit(slot Rect, w, h int64) (cell float64, at Rect) {
	w, h = max(w, 1), max(h, 1)
	cell = min(slot.W, slot.H) / float64(max(w, h))
	at = Rect{W: float64(w) * cell, H: float64(h) * cell}
	at.X, at.Y = slot.X+(slot.W-at.W)/2, slot.Y+(slot.H-at.H)/2
	return cell, at
}
