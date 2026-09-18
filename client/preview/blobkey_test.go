package preview

import (
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

func TestBlobKeyKeysPagesBySentinelAndURLsByNothing(t *testing.T) {
	cases := []struct {
		name string
		tile *pb.Tile
		want int64
	}{
		{"a captured tile keys by its blob", &pb.Tile{Kind: rpc.KindURL, PreviewBlobId: 7}, 7},
		{"a page keys by no generation", &pb.Tile{Kind: rpc.KindText, ServesPage: true}, ungeneratedBlobID},
		{"a page with a capture still keys by its blob",
			&pb.Tile{Kind: rpc.KindText, ServesPage: true, PreviewBlobId: 9}, 9},
		{"an uncaptured url has no preview to fetch", &pb.Tile{Kind: rpc.KindURL}, 0},
		// The whole point of rpc.PageContent: a url tile is never a page
		// however it is flagged, so the sentinel is not its key.
		{"a url flagged serves_page is still not a page",
			&pb.Tile{Kind: rpc.KindURL, ServesPage: true}, 0},
	}
	for _, c := range cases {
		if got := BlobKey(c.tile); got != c.want {
			t.Errorf("%s: BlobKey = %d, want %d", c.name, got, c.want)
		}
	}
}
