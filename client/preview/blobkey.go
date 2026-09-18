package preview

import (
	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// BlobKey resolves a tile's cache key, 0 meaning no preview and no fetch. The
// one keying rule for every preview draw and fetch. A page tile has no
// preview_blob_id, its plugin deriving the face from the content, so its face
// is keyed to no generation and fetched once per session.
func BlobKey(t *pb.Tile) int64 {
	if t.PreviewBlobId != 0 {
		return t.PreviewBlobId
	}
	// rpc.PageContent, not the serves_page bit: a url tile is never a page
	// however it is flagged, and keying one to the sentinel would hand it a
	// face the /content/ door never serves.
	if rpc.PageContent(t) {
		return ungeneratedBlobID
	}
	return 0
}
