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
		{"a page keys by no generation", &pb.Tile{Kind: rpc.KindURL, ServesPage: true}, ungeneratedBlobID},
		{"a page with a capture still keys by its blob",
			&pb.Tile{Kind: rpc.KindURL, ServesPage: true, PreviewBlobId: 9}, 9},
		// The whole point of rpc.PageContent: a url tile at its own address
		// has no face at the door, so the sentinel is not its key.
		{"an uncaptured url has no preview to fetch", &pb.Tile{Kind: rpc.KindURL}, 0},
		{"a text document has none either", &pb.Tile{Kind: rpc.KindText}, 0},
	}
	for _, c := range cases {
		if got := BlobKey(c.tile); got != c.want {
			t.Errorf("%s: BlobKey = %d, want %d", c.name, got, c.want)
		}
	}
}
