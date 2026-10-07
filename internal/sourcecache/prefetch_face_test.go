package sourcecache

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// faceUpstream is one connection whose landing holds the tiles a case names,
// and which records every preview it is asked for.
type faceUpstream struct {
	namespace.Namespace
	tiles []*pb.Tile

	mu    sync.Mutex
	asked []string
}

func (u *faceUpstream) Handshake(context.Context, *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	return &pb.HandshakeResponse{Plugins: []*pb.PluginInfo{rpc.ConnectionRow("u1", "", "u1/g1", "", rpc.View{})}}, nil
}

func (u *faceUpstream) GetGrid(context.Context, *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	return &pb.GetGridResponse{Grid: &pb.Grid{Id: "u1/g1"}, Tiles: u.tiles}, nil
}

func (u *faceUpstream) GetTilePreview(_ context.Context, req *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.asked = append(u.asked, req.TileId)
	return &pb.GetTilePreviewResponse{Jpeg: []byte{0xff, 0xd8}}, nil
}

// The walk asks for a face only when the tile's key names one: 0 is the
// node's word for none (pb.Tile.preview_blob_id), so asking costs the far
// node a round trip, and its plugin one more, for an answer already given.
func TestPrefetchAsksOnlyForAFaceTheKeyNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  int64
		want bool
	}{
		{"a screenshot", 7, true},
		{"a plugin's own picture", -3, true},
		{"no face", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &faceUpstream{tiles: []*pb.Tile{{
				Id: "u1/1", GridId: "u1/g1", Kind: "url", PreviewBlobId: tc.key, W: 1, H: 1,
			}}}
			cc := openLayer(t, up, filepath.Join(t.TempDir(), "cache.db"), Options{Prefetch: true})
			cc.prefetch(context.Background(), "")
			up.mu.Lock()
			defer up.mu.Unlock()
			if got := slices.Contains(up.asked, "u1/1"); got != tc.want {
				t.Errorf("walk asked for the face of a tile keyed %d: %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}
