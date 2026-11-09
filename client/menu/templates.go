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
