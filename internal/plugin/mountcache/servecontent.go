package mountcache

// Bounded ServeContent caching (issue #255, deferred from v1): the
// /content/ door — fs photos, plugin pages — now degrades stale-but-
// viewable like every other read instead of staying online-only. The
// bodies are the one genuinely unbounded class this cache touches, so
// they get their own valves: a per-entry cap (an oversized body streams
// through live, uncached) and a per-mount cap with oldest-first eviction
// (an emergency valve, not an LRU strategy — the small-data model means
// tripping it is exceptional). Only status-200 answers are remembered:
// an error page is a VERDICT, and verdicts are never served stale.

import (
	"context"
	"github.com/josephburnett/gridwell/api/gwerr"
	"io"

	"google.golang.org/grpc"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Valves (vars for tests).
var (
	// serveContentEntryCap bounds one cached door body. Larger bodies
	// stream through live and stay online-only.
	serveContentEntryCap = 32 << 20
	// serveContentMountCap bounds the servecontent table per mount;
	// oldest entries evict first when a store would exceed it.
	serveContentMountCap = int64(512 << 20)
)

func (c *Client) ServeContent(ctx context.Context, in *pb.ServeContentRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pb.ServeContentChunk], error) {
	upstream, err := c.GridwellClient.ServeContent(ctx, in, opts...)
	if err != nil {
		if gwerr.IsTransport(err) {
			if s, ok := c.loadServeContent(ctx, in.GetTileId(), in.GetSubpath()); ok {
				return s, nil
			}
		}
		return nil, err
	}
	return &teeServeStream{ServerStreamingClient: upstream, c: c, ctx: ctx,
		tileID: in.GetTileId(), subpath: in.GetSubpath()}, nil
}

// teeServeStream mirrors teeContentStream for the door: remember the
// complete body at clean EOF; before any frame, a transport failure falls
// back to the cached entry (the dark mount surfaces on the FIRST Recv of
// a chained stream, exactly like ReadContent).
type teeServeStream struct {
	grpc.ServerStreamingClient[pb.ServeContentChunk]
	c               *Client
	ctx             context.Context
	tileID, subpath string

	status    int64
	mediaType string
	data      []byte
	oversized bool
	stored    bool
	gotFrame  bool
	fallback  *memServeStream
}

func (s *teeServeStream) Recv() (*pb.ServeContentChunk, error) {
	if s.fallback != nil {
		return s.fallback.Recv()
	}
	chunk, err := s.ServerStreamingClient.Recv()
	if err == io.EOF {
		if !s.stored && !s.oversized && s.status == 200 {
			s.stored = true
			s.c.storeServeContent(s.ctx, s.tileID, s.subpath, s.status, s.mediaType, s.data)
		}
		return nil, err
	}
	if err != nil {
		if !s.gotFrame && gwerr.IsTransport(err) {
			if m, ok := s.c.loadServeContent(s.ctx, s.tileID, s.subpath); ok {
				s.fallback = m
				return s.fallback.Recv()
			}
		}
		return nil, err
	}
	s.gotFrame = true
	if s.status == 0 && chunk.GetStatus() != 0 {
		s.status = chunk.GetStatus()
		s.mediaType = chunk.GetMediaType()
	}
	if !s.oversized {
		s.data = append(s.data, chunk.GetData()...)
		if len(s.data) > serveContentEntryCap {
			s.oversized = true
			s.data = nil
		}
	}
	return chunk, nil
}

func (c *Client) storeServeContent(ctx context.Context, tileID, subpath string, status int64, mediaType string, data []byte) {
	_, err := c.db.ExecContext(ctx, `INSERT INTO servecontent (tile_id, subpath, status, media_type, data, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(tile_id, subpath) DO UPDATE SET status=excluded.status,
			media_type=excluded.media_type, data=excluded.data, fetched_at=excluded.fetched_at`,
		tileID, subpath, status, mediaType, data, now())
	logErr("store servecontent", err)
	c.evictServeContent(ctx)
}

// evictServeContent drops oldest entries until the table fits the mount
// cap — the emergency valve.
func (c *Client) evictServeContent(ctx context.Context) {
	for i := 0; i < 64; i++ { // hard stop; each pass drops one entry
		var total int64
		if err := c.db.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(LENGTH(data)),0) FROM servecontent`).Scan(&total); err != nil {
			logErr("evict servecontent", err)
			return
		}
		if total <= serveContentMountCap {
			return
		}
		if _, err := c.db.ExecContext(ctx, `DELETE FROM servecontent WHERE rowid =
			(SELECT rowid FROM servecontent ORDER BY fetched_at ASC, rowid ASC LIMIT 1)`); err != nil {
			logErr("evict servecontent", err)
			return
		}
	}
}

func (c *Client) loadServeContent(ctx context.Context, tileID, subpath string) (*memServeStream, bool) {
	var status int64
	var mediaType string
	var data []byte
	err := c.db.QueryRowContext(ctx, `SELECT status, media_type, data FROM servecontent
		WHERE tile_id = ? AND subpath = ?`, tileID, subpath).Scan(&status, &mediaType, &data)
	if err != nil {
		return nil, false
	}
	return newMemServeStream(ctx, status, mediaType, data), true
}

// memServeStream serves a cached door body in the live chunk shape:
// chunk 1 carries status + media_type, later chunks data only.
type memServeStream struct {
	noopClientStream
	chunks []*pb.ServeContentChunk
	i      int
}

func newMemServeStream(ctx context.Context, status int64, mediaType string, data []byte) *memServeStream {
	first := &pb.ServeContentChunk{Status: status, MediaType: mediaType}
	if len(data) > 0 {
		first.Data = data[:min(len(data), contentChunkBytes)]
		data = data[len(first.Data):]
	}
	chunks := []*pb.ServeContentChunk{first}
	for len(data) > 0 {
		n := min(len(data), contentChunkBytes)
		chunks = append(chunks, &pb.ServeContentChunk{Data: data[:n]})
		data = data[n:]
	}
	return &memServeStream{noopClientStream: noopClientStream{ctx: ctx}, chunks: chunks}
}

func (m *memServeStream) Recv() (*pb.ServeContentChunk, error) {
	if m.i >= len(m.chunks) {
		return nil, io.EOF
	}
	ch := m.chunks[m.i]
	m.i++
	return ch, nil
}
