package sourcecache

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// linkUpstream is a fake remote node, with qualified ids, the shape a
// connection really serves: a root grid holding one leaf link whose target
// lives in a grid no well references. The link arm is the target's only warm
// path, which is the case the walker promises to cover.
type linkUpstream struct {
	namespace.Namespace
	dark bool
}

func (u *linkUpstream) offline() error { return status.Error(codes.Unavailable, "tunnel down") }

func (u *linkUpstream) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	if u.dark {
		return nil, u.offline()
	}
	return &pb.InfoResponse{Kind: "remote", RootGridId: "u1/g1"}, nil
}

func (u *linkUpstream) Handshake(context.Context, *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	if u.dark {
		return nil, u.offline()
	}
	return &pb.HandshakeResponse{}, nil
}

func (u *linkUpstream) GetGrid(_ context.Context, req *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	if u.dark {
		return nil, u.offline()
	}
	if req.GridId != "u1/g1" {
		return nil, status.Error(codes.NotFound, "no grid")
	}
	return &pb.GetGridResponse{
		Grid: &pb.Grid{Id: "u1/g1"},
		Tiles: []*pb.Tile{{
			Id: "u1/1", GridId: "u1/g1", Kind: "text",
			LinkTargetId: "u1/2", Reference: true,
			X: 0, Y: 0, W: 1, H: 1,
		}},
	}, nil
}

func (u *linkUpstream) GetTile(_ context.Context, req *pb.GetTileRequest) (*pb.TileResponse, error) {
	if u.dark {
		return nil, u.offline()
	}
	switch req.TileId {
	case "u1/1":
		return &pb.TileResponse{Tile: &pb.Tile{Id: "u1/1", GridId: "u1/g1", Kind: "text", LinkTargetId: "u1/2", Reference: true}}, nil
	case "u1/2":
		return &pb.TileResponse{Tile: &pb.Tile{Id: "u1/2", GridId: "u1/g2", Kind: "text"}}, nil
	}
	return nil, status.Error(codes.NotFound, "no tile")
}

func (u *linkUpstream) GetTilePreview(_ context.Context, _ *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	if u.dark {
		return nil, u.offline()
	}
	return &pb.GetTilePreviewResponse{}, nil
}

func (u *linkUpstream) ReadContent(_ context.Context, req *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	if u.dark {
		return u.offline()
	}
	switch req.TileId {
	case "u1/1", "u1/2":
		return send(&pb.ContentChunk{Data: []byte("linked note"), MediaType: "text/markdown"})
	}
	return status.Error(codes.NotFound, "no content")
}

// The link arm must not pre-mark the target seen before recursing: walkTile
// would then return at its own seen-check without warming anything, leaving
// the target's body offline-unreadable after a whole-source prefetch whenever
// the link was its only path.
func TestPrefetchWarmsLinkTargetBody(t *testing.T) {
	up := &linkUpstream{}
	cc := openLayer(t, up, filepath.Join(t.TempDir(), "cache.db"), Options{Prefetch: true})

	ctx := context.Background()
	cc.Prefetch(ctx)
	up.dark = true

	_, _, data := readContent(t, cc, "u1/2")
	if !bytes.Equal(data, []byte("linked note")) {
		t.Errorf("link target body = %q, want the linked note", data)
	}
}
