package menu

// The templates: the tiles on home's templates grid, drawn in the strip
// before the swatches, after the copy swatch.

import (
	"cmp"
	"math"
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

// TemplateCell is where a new template h tall lands on the templates grid:
// at column 0 above every tile there, so the grid opened as a place reads
// newest first as the strip does, and no cell it takes is held.
func TemplateCell(tiles map[string]*gridwellv1.Tile, h int64) (x, y int64) {
	if len(tiles) == 0 {
		return 0, 0
	}
	top := int64(math.MaxInt64)
	for _, t := range tiles {
		top = min(top, t.Y)
	}
	return 0, top - max(h, 1)
}
