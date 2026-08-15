package mountcache

// The three stream shapes behind the cache: a TEE over a live ReadContent
// (remember bytes as they pass), a TEE over the Subscribe event stream
// (track the live session's mutations), and a MEMORY stream that serves a
// cached body in the exact chunk shape a live plugin sends — so a caller
// cannot tell a remembered answer from a live one by its framing.

import (
	"context"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// teeContentStream accumulates a live ReadContent as it streams through
// and persists it at clean EOF. A mid-stream failure (or an oversized
// body) skips the store — the cache holds complete values only; a partial
// body served later would be silent corruption.
//
// The FIRST Recv is also where a dark mount actually surfaces on a
// chained read: the stream OPEN only reaches the local transit plugin
// (alive), and the remote's unreachability arrives as the first frame's
// error — the real federation gate found this shape; a unit fixture
// whose open fails directly never could. A transport-shaped failure
// BEFORE any frame falls back to the cached body; after a frame has
// flowed, the error passes through (splicing cache into a half-live
// stream would fabricate a body nobody ever had).
type teeContentStream struct {
	grpc.ServerStreamingClient[pb.ContentChunk]
	c      *Client
	ctx    context.Context
	tileID string

	mediaType string
	version   int64
	data      []byte
	oversized bool
	stored    bool
	gotFrame  bool
	fallback  *memContentStream
}

func (s *teeContentStream) Recv() (*pb.ContentChunk, error) {
	if s.fallback != nil {
		return s.fallback.Recv()
	}
	chunk, err := s.ServerStreamingClient.Recv()
	if err == io.EOF {
		if !s.stored && !s.oversized {
			s.stored = true
			s.c.storeContent(s.ctx, s.tileID, s.mediaType, s.version, s.data)
		}
		return nil, err
	}
	if err != nil {
		if !s.gotFrame && unreachable(err) {
			if mt, ver, data, ok := s.c.loadContent(s.ctx, s.tileID); ok {
				s.fallback = newMemContentStream(s.ctx, mt, ver, data)
				return s.fallback.Recv()
			}
		}
		return nil, err
	}
	s.gotFrame = true
	// Chunk 1 carries media_type + version (a plugin sends it even for
	// empty content, so both always arrive before EOF).
	if s.mediaType == "" && chunk.GetMediaType() != "" {
		s.mediaType = chunk.GetMediaType()
	}
	if s.version == 0 && chunk.GetVersion() != 0 {
		s.version = chunk.GetVersion()
	}
	if !s.oversized {
		s.data = append(s.data, chunk.GetData()...)
		if len(s.data) > maxCachedContentBytes {
			s.oversized = true
			s.data = nil
		}
	}
	return chunk, nil
}

// teeEventStream folds each mount event into the cache on its way to the
// server's fan-in.
type teeEventStream struct {
	grpc.ServerStreamingClient[pb.Event]
	c   *Client
	ctx context.Context
}

func (s *teeEventStream) Recv() (*pb.Event, error) {
	ev, err := s.ServerStreamingClient.Recv()
	if err != nil {
		return nil, err
	}
	s.c.applyEvent(s.ctx, ev)
	return ev, nil
}

// memContentStream serves a cached body as a ReadContent stream: chunk 1
// carries media_type + version (exactly the live contract — the version
// travels WITH the bytes it vouches for), later chunks carry data only.
type memContentStream struct {
	noopClientStream
	chunks []*pb.ContentChunk
	i      int
}

func newMemContentStream(ctx context.Context, mediaType string, version int64, data []byte) *memContentStream {
	first := &pb.ContentChunk{MediaType: mediaType, Version: version}
	if len(data) > 0 {
		first.Data = data[:min(len(data), contentChunkBytes)]
		data = data[len(first.Data):]
	}
	chunks := []*pb.ContentChunk{first}
	for len(data) > 0 {
		n := min(len(data), contentChunkBytes)
		chunks = append(chunks, &pb.ContentChunk{Data: data[:n]})
		data = data[n:]
	}
	return &memContentStream{noopClientStream: noopClientStream{ctx: ctx}, chunks: chunks}
}

func (m *memContentStream) Recv() (*pb.ContentChunk, error) {
	if m.i >= len(m.chunks) {
		return nil, io.EOF
	}
	ch := m.chunks[m.i]
	m.i++
	return ch, nil
}

// noopClientStream satisfies the grpc.ClientStream surface for a stream
// that was never on a wire.
type noopClientStream struct{ ctx context.Context }

func (noopClientStream) Header() (metadata.MD, error) { return nil, nil }
func (noopClientStream) Trailer() metadata.MD         { return nil }
func (noopClientStream) CloseSend() error             { return nil }
func (n noopClientStream) Context() context.Context   { return n.ctx }
func (noopClientStream) SendMsg(any) error            { return nil }
func (noopClientStream) RecvMsg(any) error            { return io.EOF }
