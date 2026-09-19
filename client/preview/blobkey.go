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
	// A url tile at its own address has no door face, so the sentinel would
	// send its fetch somewhere the /content/ door never answers.
	if rpc.PageContent(t) {
		return ungeneratedBlobID
	}
	return 0
}
