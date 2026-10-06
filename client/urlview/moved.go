package urlview

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Moved is what a url tile's changed row asks of this client's own copies of
// its page.
type Moved struct {
	// Reload: a live view of the tile loads its page again once it is on
	// screen, as the descent does.
	Reload bool
	// DropCapture: a frame this client captured no longer answers for the
	// face, because the node retired the screenshot it would have become
	// (pluginhost.pageFaceStale).
	DropCapture bool
}

// PageMoved reads one TileChanged. Only a page its plugin serves is told it
// moved; a page on the web has nobody to say so. A frozen tile keeps the
// screenshot the user took and has no live view.
func PageMoved(t *gridwellv1.Tile, contentChanged bool) Moved {
	if !contentChanged || !rpc.PageContent(t) || t.UrlFrozen {
		return Moved{}
	}
	return Moved{Reload: true, DropCapture: t.PreviewBlobId <= 0}
}
