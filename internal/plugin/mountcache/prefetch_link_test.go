package mountcache

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// linkUpstream is a fake REMOTE NODE (qualified ids, the shape a mount
// really serves): a root grid holding one leaf link whose target lives
// in a grid no well references — the link arm is the target's ONLY warm
// path, exactly the case the walker's doc promises to cover ("warm the
// target row and its face/body even if its own grid is never walked").
type linkUpstream struct {
	pb.GridwellClient
	dark bool
}

func (u *linkUpstream) offline() error { return status.Error(codes.Unavailable, "tunnel down") }

func (u *linkUpstream) Info(context.Context, *pb.InfoRequest, ...grpc.CallOption) (*pb.InfoResponse, error) {
	if u.dark {
		return nil, u.offline()
	}
	return &pb.InfoResponse{Kind: "remote", RootGridId: "u1/g1"}, nil
}

func (u *linkUpstream) ListPlugins(context.Context, *pb.ListPluginsRequest, ...grpc.CallOption) (*pb.ListPluginsResponse, error) {
	if u.dark {
		return nil, u.offline()
	}
	return &pb.ListPluginsResponse{}, nil
}

func (u *linkUpstream) GetGrid(_ context.Context, req *pb.GetGridRequest, _ ...grpc.CallOption) (*pb.GetGridResponse, error) {
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

func (u *linkUpstream) GetTile(_ context.Context, req *pb.GetTileRequest, _ ...grpc.CallOption) (*pb.TileResponse, error) {
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

func (u *linkUpstream) GetTilePreview(_ context.Context, _ *pb.GetTilePreviewRequest, _ ...grpc.CallOption) (*pb.GetTilePreviewResponse, error) {
	if u.dark {
		return nil, u.offline()
	}
	return &pb.GetTilePreviewResponse{}, nil
}

func (u *linkUpstream) ReadContent(_ context.Context, req *pb.ReadContentRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[pb.ContentChunk], error) {
	if u.dark {
		return nil, u.offline()
	}
	switch req.TileId {
	case "u1/1", "u1/2":
		return &chunkStream{chunks: []*pb.ContentChunk{{Data: []byte("linked note"), MediaType: "text/markdown"}}}, nil
	}
	return nil, status.Error(codes.NotFound, "no content")
}

type chunkStream struct {
	grpc.ClientStream
	chunks []*pb.ContentChunk
}

func (s *chunkStream) Recv() (*pb.ContentChunk, error) {
	if len(s.chunks) == 0 {
		return nil, io.EOF
	}
	ch := s.chunks[0]
	s.chunks = s.chunks[1:]
	return ch, nil
}

// The link arm used to pre-mark the target seen before recursing, so
// walkTile returned at its own seen-check without warming anything —
// the target's body was offline-unreadable after a "whole-mount"
// prefetch whenever the link was the only path to it.
func TestPrefetchWarmsLinkTargetBody(t *testing.T) {
	up := &linkUpstream{}
	cc, dbClose, err := Open(up, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dbClose)

	ctx := context.Background()
	cc.Prefetch(ctx)
	up.dark = true

	s, err := cc.ReadContent(ctx, &pb.ReadContentRequest{TileId: "u1/2"})
	if err != nil {
		t.Fatalf("link target's body must be prefetched: %v", err)
	}
	_, _, data := drainContent(t, s)
	if !bytes.Equal(data, []byte("linked note")) {
		t.Errorf("link target body = %q, want the linked note", data)
	}
}
