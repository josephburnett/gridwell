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

// ServeContent tees the door body the way ReadContent tees a tile's:
// remember the complete body at a clean end; a transport failure before
// any chunk falls back to the remembered entry. Only status-200 answers
// are remembered — an error page is a VERDICT, never served stale.
func (c *Client) ServeContent(ctx context.Context, in *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error {
	var status int64
	var mediaType string
	var data []byte
	var gotChunk, oversized bool
	err := c.Namespace.ServeContent(ctx, in, func(ch *pb.ServeContentChunk) error {
		gotChunk = true
		if status == 0 && ch.GetStatus() != 0 {
			status = ch.GetStatus()
			mediaType = ch.GetMediaType()
		}
		if !oversized {
			data = append(data, ch.GetData()...)
			if len(data) > serveContentEntryCap {
				oversized = true
				data = nil
			}
		}
		return send(ch)
	})
	if err == nil {
		if !oversized && status == 200 {
			c.storeServeContent(ctx, in.GetTileId(), in.GetSubpath(), status, mediaType, data)
		}
		return nil
	}
	if !gotChunk && gwerr.IsTransport(err) {
		if st, mt, cached, ok := c.loadServeContent(ctx, in.GetTileId(), in.GetSubpath()); ok {
			return sendChunked(cached, func(b []byte, first bool) error {
				if first {
					return send(&pb.ServeContentChunk{Status: st, MediaType: mt, Data: b})
				}
				return send(&pb.ServeContentChunk{Data: b})
			})
		}
	}
	return err
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

func (c *Client) loadServeContent(ctx context.Context, tileID, subpath string) (status int64, mediaType string, data []byte, ok bool) {
	err := c.db.QueryRowContext(ctx, `SELECT status, media_type, data FROM servecontent
		WHERE tile_id = ? AND subpath = ?`, tileID, subpath).Scan(&status, &mediaType, &data)
	if err != nil {
		return 0, "", nil, false
	}
	return status, mediaType, data, true
}
