package nav

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/contentzoom"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/pane"
)

// GridRows is a held grid's tiles and whether its bodies take edits, by
// value, so the machine reads what a paint of it will ask for.
type GridRows struct {
	Tiles    []*gridwellv1.Tile
	Writable bool
}

// landing is where a descent will land: the pane's rect and the view there.
type landing struct {
	Rect pane.Rect
	View rpc.Framing
}

// landingReads asks, as soon as rows are held, for what the first paint of
// them at the landing reads beyond the grid: the bodies of the texts and
// workspace tiles it shows, the previews of its url and shell tiles, and the
// grids its doors preview. The paint asks the same and finds them in flight,
// so the round trip runs under the transition rather than after it. A link's
// text is left to the paint, since whether its target shows a body is the
// target row's to say.
func (p *planner) landingReads(w World, rows *GridRows, at landing) {
	scr, ok := dragdrop.NewPane(at.Rect.X, at.Rect.Y, at.Rect.W, at.Rect.H, at.View, w.CellPx)
	if rows == nil || !ok {
		return
	}
	cell := scr.Cell()
	for _, n := range rows.Tiles {
		if !scr.Shows(float64(n.X), float64(n.Y), float64(n.W), float64(n.H)) {
			continue
		}
		h := float64(n.H) * cell
		switch k := rpc.KindOf(n.Kind); {
		case k.Content == rpc.ContentBody:
			scale := w.TextScale * contentzoom.Of(n.GetContentZoom())
			if !rpc.LeafLink(n) && n.GetTextPresentation() != rpc.TextPresentationInfo && markdown.PreviewBodyShown(h, scale) {
				p.add(Effect{Kind: EffFetchTileContent, TileID: n.Id})
			}
		case k.Content == rpc.ContentLayout:
			p.add(Effect{Kind: EffFetchTileContent, TileID: n.Id})
		case k.Live == rpc.DescentURL || k.Live == rpc.DescentShell:
			if n.PreviewBlobId != 0 {
				p.add(Effect{Kind: EffFetchPreview, TileID: rpc.ContentID(n), BlobID: n.PreviewBlobId})
			}
		case k.Descends == rpc.DescendGrid:
			if door.PreviewAsked(n, rows.Writable) && !w.current(n.ChildGridId) {
				p.add(Effect{Kind: EffFetchGrid, GridID: n.ChildGridId, Preview: true})
			}
		}
	}
}
