package sourcecache

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// budgetFar serves one root grid of two text tiles and a served page, each
// body bodySize bytes, and records which bodies the walk read.
type budgetFar struct {
	namespace.Namespace
	read []string
}

const bodySize = 100

func (b *budgetFar) Handshake(context.Context, *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	return &pb.HandshakeResponse{Sources: []*pb.SourceInfo{rpc.ConnectionRow("u1", "", "u1/g1", "", rpc.View{})}}, nil
}

func (b *budgetFar) GetGrid(context.Context, *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	return &pb.GetGridResponse{Grid: &pb.Grid{Id: "u1/g1"}, Tiles: []*pb.Tile{
		{Id: "u1/1", GridId: "u1/g1", Kind: rpc.KindText, W: 1, H: 1},
		{Id: "u1/2", GridId: "u1/g1", Kind: rpc.KindText, X: 1, W: 1, H: 1},
		{Id: "u1/3", GridId: "u1/g1", Kind: rpc.KindURL, ServesPage: true, X: 2, W: 1, H: 1},
	}}, nil
}

func (b *budgetFar) ReadContent(_ context.Context, req *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	b.read = append(b.read, req.TileId)
	return send(&pb.ContentChunk{Data: make([]byte, bodySize), MediaType: "text/markdown"})
}

func (b *budgetFar) ServeContent(_ context.Context, req *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error {
	b.read = append(b.read, req.TileId)
	return send(&pb.ServeContentChunk{Data: make([]byte, bodySize), MediaType: "image/png"})
}

// The walk reads bodies, a text's and a served page's alike, until what it
// read reaches prefetchContentBudget, and none after.
func TestPrefetchStopsReadingBodiesAtTheBudget(t *testing.T) {
	for _, c := range []struct {
		budget int
		want   []string
	}{
		{bodySize, []string{"u1/1"}},
		{2 * bodySize, []string{"u1/1", "u1/2"}},
		{3 * bodySize, []string{"u1/1", "u1/2", "u1/3"}},
	} {
		was := prefetchContentBudget
		prefetchContentBudget = c.budget
		far := &budgetFar{}
		cc := openLayer(t, far, filepath.Join(t.TempDir(), "cache.db"), Options{Prefetch: true})
		cc.prefetch(context.Background(), "")
		prefetchContentBudget = was
		if !slices.Equal(far.read, c.want) {
			t.Errorf("budget %d: the walk read %v, want %v", c.budget, far.read, c.want)
		}
	}
}
