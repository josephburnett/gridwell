// Package mountcache is the node-side read-through cache in front of a
// MOUNT (a transit plugin — ssh), phase 1 of docs/offline-plan.md: a
// mounted machine going dark degrades to STALE-BUT-READABLE instead of
// blank. It wraps the mount's gRPC client; online it passes through and
// remembers every successful read; when the mount is unreachable
// (transport-class failure ONLY — an answered "gone" is never masked) it
// serves the remembered answer. Writes always pass through: the cache is
// never a write buffer, and the remote stays the one owner of its truth —
// this layer owns only the REMEMBERED ANSWER (charter §7: one fact, one
// owner; no second writer).
//
// Storage is one SQLite DB per mount, EXPLICITLY DISPOSABLE: deleting it
// is always safe (it re-warms from use), it is not backed up, and it is
// NOT under the frozen-format promise — dbformat-versioned only so a
// future shape change can migrate or refuse cleanly. Rows are wire-shaped
// (marshaled protos keyed by the mount-local ids this wrapper sees), so
// there is no schema to drift against the contract.
//
// Freshness: every successful read replaces its rows, and the Subscribe
// stream the server already holds through this wrapper is teed — a
// TileChanged upserts, a TileRemoved deletes — so the cache tracks the
// live session without a poller. Deletions that happen while the mount is
// DARK are caught by the next successful GetGrid (which replaces the
// grid's whole tile set); that read-through refresh is the resync, by
// construction rather than by a sweep.
//
// Not cached (deliberate): write responses (the next read refreshes).
// ServeContent bodies are cached BOUNDED (servecontent.go, issue #255);
// whole-mount prefetch warms everything else on connect (prefetch.go,
// issue #254).
package mountcache

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/josephburnett/gridwell/api/gwerr"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/dbformat"
	_ "modernc.org/sqlite"
)

// cacheApplicationID stamps a mount-cache file as ours ("gwmc"), so a
// foreign SQLite file at the cache path is refused, never overwritten.
const cacheApplicationID int64 = 0x67776d63

// cacheSchemaVersion is the current cache generation. The file is
// disposable, but versioning still refuses newer files and migrates older
// ones instead of corrupting either.
const cacheSchemaVersion = 1

const schemaDDL = `
CREATE TABLE IF NOT EXISTS info (
    k     TEXT PRIMARY KEY,
    proto BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS pluginlists (
    ns    TEXT PRIMARY KEY,
    proto BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS grids (
    id         TEXT PRIMARY KEY,
    proto      BLOB NOT NULL,
    fetched_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tiles (
    id         TEXT PRIMARY KEY,
    grid_id    TEXT NOT NULL,
    proto      BLOB NOT NULL,
    fetched_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mountcache_tiles_grid ON tiles(grid_id);
CREATE TABLE IF NOT EXISTS content (
    tile_id    TEXT PRIMARY KEY,
    media_type TEXT NOT NULL,
    version    INTEGER NOT NULL,
    data       BLOB NOT NULL,
    fetched_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS previews (
    tile_id    TEXT PRIMARY KEY,
    jpeg       BLOB NOT NULL,
    fetched_at INTEGER NOT NULL
);
-- The /content/ door's bounded body cache (issue #255). Added without a
-- version bump: schemaDDL runs at every Open and the table is additive,
-- which is exactly the liberty the disposable, non-frozen format buys.
CREATE TABLE IF NOT EXISTS servecontent (
    tile_id    TEXT NOT NULL,
    subpath    TEXT NOT NULL,
    status     INTEGER NOT NULL,
    media_type TEXT NOT NULL,
    data       BLOB NOT NULL,
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (tile_id, subpath)
);
`

// contentChunkBytes mirrors the plugins' ReadContent chunking so a
// cache-served stream is shaped like a live one.
const contentChunkBytes = 256 * 1024

// maxCachedContentBytes bounds one cached body. Matches the store's blob
// cap — anything a plugin can serve as tile content fits; a larger stream
// still passes through live, just uncached.
const maxCachedContentBytes = 16 * 1024 * 1024

// Client wraps a mount's gRPC client with the read-through cache. All
// methods not overridden here pass through the embedded client untouched
// (writes, shells, Probe, SetFraming).
type Client struct {
	pb.GridwellClient
	db *sql.DB
	pf prefetcher
}

// Open opens (or creates) the cache DB at dbPath and returns the wrapped
// client. Close the returned closer with the plugin's own.
func Open(upstream pb.GridwellClient, dbPath string) (*Client, func(), error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("mountcache open %s: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("mountcache pragmas: %w", err)
	}
	if _, err := db.Exec(schemaDDL); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("mountcache schema: %w", err)
	}
	if err := dbformat.EnsureVersion(context.Background(), db, cacheApplicationID, cacheSchemaVersion, nil); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("mountcache %s: %w", dbPath, err)
	}
	c := &Client{GridwellClient: upstream, db: db}
	c.pf.ctx, c.pf.cancel = context.WithCancel(context.Background())
	return c, func() {
		c.pf.cancel()
		c.pf.wg.Wait() // the walk is out before its DB goes away
		_ = db.Close()
	}, nil
}

// logErr surfaces a cache-side failure to the server log. A broken cache
// must never fail a live request — but it must not be silent either, or
// the offline promise degrades invisibly until the day it's needed.
func logErr(op string, err error) {
	if err != nil {
		log.Printf("gridwell: mountcache %s: %v (cache degraded; the live path is unaffected)", op, err)
	}
}

func now() int64 { return time.Now().Unix() }

// ── Info ────────────────────────────────────────────────────────────────

func (c *Client) Info(ctx context.Context, in *pb.InfoRequest, opts ...grpc.CallOption) (*pb.InfoResponse, error) {
	resp, err := c.GridwellClient.Info(ctx, in, opts...)
	if err == nil {
		if b, merr := proto.Marshal(resp); merr == nil {
			_, werr := c.db.ExecContext(ctx, `INSERT INTO info (k, proto) VALUES ('info', ?)
				ON CONFLICT(k) DO UPDATE SET proto=excluded.proto`, b)
			logErr("store info", werr)
		}
		return resp, nil
	}
	if !gwerr.IsTransport(err) {
		return nil, err
	}
	var b []byte
	if serr := c.db.QueryRowContext(ctx, `SELECT proto FROM info WHERE k='info'`).Scan(&b); serr != nil {
		return nil, err // miss: the original transport error stands
	}
	cached := &pb.InfoResponse{}
	if uerr := proto.Unmarshal(b, cached); uerr != nil {
		return nil, err
	}
	return cached, nil
}

// Handshake forwards the ROUTED plugin list (remote-menu, 2026-08-16)
// and remembers the answer per namespace, so a remote pane's + menu is
// readable while the mount is dark — same contract as every other read:
// serve-stale on transport only, verdicts pass through.
func (c *Client) Handshake(ctx context.Context, in *pb.HandshakeRequest, opts ...grpc.CallOption) (*pb.HandshakeResponse, error) {
	resp, err := c.GridwellClient.Handshake(ctx, in, opts...)
	if err == nil {
		if b, merr := proto.Marshal(resp); merr == nil {
			_, werr := c.db.ExecContext(ctx, `INSERT INTO pluginlists (ns, proto) VALUES (?, ?)
				ON CONFLICT(ns) DO UPDATE SET proto=excluded.proto`, in.GetNamespace(), b)
			logErr("store pluginlist", werr)
		}
		return resp, nil
	}
	if !gwerr.IsTransport(err) {
		return nil, err
	}
	var b []byte
	if serr := c.db.QueryRowContext(ctx, `SELECT proto FROM pluginlists WHERE ns = ?`, in.GetNamespace()).Scan(&b); serr != nil {
		return nil, err
	}
	cached := &pb.HandshakeResponse{}
	if uerr := proto.Unmarshal(b, cached); uerr != nil {
		return nil, err
	}
	return cached, nil
}

// ── GetGrid ─────────────────────────────────────────────────────────────

func (c *Client) GetGrid(ctx context.Context, in *pb.GetGridRequest, opts ...grpc.CallOption) (*pb.GetGridResponse, error) {
	resp, err := c.GridwellClient.GetGrid(ctx, in, opts...)
	if err == nil {
		c.storeGrid(ctx, in.GridId, resp)
		return resp, nil
	}
	if !gwerr.IsTransport(err) {
		return nil, err
	}
	cached, hit := c.loadGrid(ctx, in.GridId)
	if !hit {
		return nil, err
	}
	// The stale bit (issue #256): this is the REMEMBERED answer, and the
	// wire says so — the one place the fact is known is the one place it
	// is stamped. Wire-only, never stored (a later live read re-stores
	// the grid without it).
	if cached.GetGrid() != nil {
		cached.Grid.Stale = true
	}
	return cached, nil
}

// storeGrid replaces the grid row AND its whole tile set in one
// transaction — a successful GetGrid is by definition the complete list,
// so this is also how deletions that happened while dark reconcile.
func (c *Client) storeGrid(ctx context.Context, gridID string, resp *pb.GetGridResponse) {
	gb, err := proto.Marshal(resp.GetGrid())
	if err != nil {
		logErr("marshal grid", err)
		return
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		logErr("store grid", err)
		return
	}
	ts := now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO grids (id, proto, fetched_at) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET proto=excluded.proto, fetched_at=excluded.fetched_at`, gridID, gb, ts); err != nil {
		logErr("store grid", err)
		_ = tx.Rollback()
		return
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tiles WHERE grid_id = ?`, gridID); err != nil {
		logErr("store grid", err)
		_ = tx.Rollback()
		return
	}
	for _, t := range resp.GetTiles() {
		tb, merr := proto.Marshal(t)
		if merr != nil {
			logErr("marshal tile", merr)
			_ = tx.Rollback()
			return
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tiles (id, grid_id, proto, fetched_at) VALUES (?, ?, ?, ?)`,
			t.GetId(), gridID, tb, ts); err != nil {
			logErr("store tile", err)
			_ = tx.Rollback()
			return
		}
	}
	logErr("store grid", tx.Commit())
}

func (c *Client) loadGrid(ctx context.Context, gridID string) (*pb.GetGridResponse, bool) {
	var gb []byte
	if err := c.db.QueryRowContext(ctx, `SELECT proto FROM grids WHERE id = ?`, gridID).Scan(&gb); err != nil {
		return nil, false
	}
	g := &pb.Grid{}
	if err := proto.Unmarshal(gb, g); err != nil {
		return nil, false
	}
	rows, err := c.db.QueryContext(ctx, `SELECT proto FROM tiles WHERE grid_id = ?`, gridID)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	resp := &pb.GetGridResponse{Grid: g}
	for rows.Next() {
		var tb []byte
		if err := rows.Scan(&tb); err != nil {
			return nil, false
		}
		t := &pb.Tile{}
		if err := proto.Unmarshal(tb, t); err != nil {
			return nil, false
		}
		resp.Tiles = append(resp.Tiles, t)
	}
	return resp, rows.Err() == nil
}

// ── GetTile ─────────────────────────────────────────────────────────────

func (c *Client) GetTile(ctx context.Context, in *pb.GetTileRequest, opts ...grpc.CallOption) (*pb.TileResponse, error) {
	resp, err := c.GridwellClient.GetTile(ctx, in, opts...)
	if err == nil {
		c.upsertTile(ctx, resp.GetTile())
		return resp, nil
	}
	if !gwerr.IsTransport(err) {
		return nil, err
	}
	var tb []byte
	if serr := c.db.QueryRowContext(ctx, `SELECT proto FROM tiles WHERE id = ?`, in.TileId).Scan(&tb); serr != nil {
		return nil, err
	}
	t := &pb.Tile{}
	if uerr := proto.Unmarshal(tb, t); uerr != nil {
		return nil, err
	}
	return &pb.TileResponse{Tile: t}, nil
}

func (c *Client) upsertTile(ctx context.Context, t *pb.Tile) {
	if t == nil || t.GetId() == "" {
		return
	}
	tb, err := proto.Marshal(t)
	if err != nil {
		logErr("marshal tile", err)
		return
	}
	_, werr := c.db.ExecContext(ctx, `INSERT INTO tiles (id, grid_id, proto, fetched_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET grid_id=excluded.grid_id, proto=excluded.proto, fetched_at=excluded.fetched_at`,
		t.GetId(), t.GetGridId(), tb, now())
	logErr("upsert tile", werr)
}

func (c *Client) deleteTile(ctx context.Context, tileID string) {
	_, err := c.db.ExecContext(ctx, `DELETE FROM tiles WHERE id = ?`, tileID)
	logErr("delete tile", err)
	_, err = c.db.ExecContext(ctx, `DELETE FROM content WHERE tile_id = ?`, tileID)
	logErr("delete content", err)
	_, err = c.db.ExecContext(ctx, `DELETE FROM previews WHERE tile_id = ?`, tileID)
	logErr("delete preview", err)
}

// ── GetTilePreview ──────────────────────────────────────────────────────

func (c *Client) GetTilePreview(ctx context.Context, in *pb.GetTilePreviewRequest, opts ...grpc.CallOption) (*pb.GetTilePreviewResponse, error) {
	resp, err := c.GridwellClient.GetTilePreview(ctx, in, opts...)
	if err == nil {
		if jpeg := resp.GetJpeg(); len(jpeg) > 0 {
			_, werr := c.db.ExecContext(ctx, `INSERT INTO previews (tile_id, jpeg, fetched_at) VALUES (?, ?, ?)
				ON CONFLICT(tile_id) DO UPDATE SET jpeg=excluded.jpeg, fetched_at=excluded.fetched_at`,
				in.TileId, jpeg, now())
			logErr("store preview", werr)
		}
		return resp, nil
	}
	if !gwerr.IsTransport(err) {
		return nil, err
	}
	var jpeg []byte
	if serr := c.db.QueryRowContext(ctx, `SELECT jpeg FROM previews WHERE tile_id = ?`, in.TileId).Scan(&jpeg); serr != nil {
		return nil, err
	}
	return &pb.GetTilePreviewResponse{Jpeg: jpeg}, nil
}

// ── ReadContent ─────────────────────────────────────────────────────────

func (c *Client) ReadContent(ctx context.Context, in *pb.ReadContentRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pb.ContentChunk], error) {
	stream, err := c.GridwellClient.ReadContent(ctx, in, opts...)
	if err == nil {
		// Tee: remember the bytes as they stream through. (The open
		// itself succeeding does not mean the stream will — a mid-stream
		// transport failure surfaces to the caller AND skips the store.)
		return &teeContentStream{ServerStreamingClient: stream, c: c, ctx: ctx, tileID: in.TileId}, nil
	}
	if !gwerr.IsTransport(err) {
		return nil, err
	}
	mediaType, version, data, ok := c.loadContent(ctx, in.TileId)
	if !ok {
		return nil, err
	}
	return newMemContentStream(ctx, mediaType, version, data), nil
}

func (c *Client) loadContent(ctx context.Context, tileID string) (mediaType string, version int64, data []byte, ok bool) {
	if err := c.db.QueryRowContext(ctx, `SELECT media_type, version, data FROM content WHERE tile_id = ?`, tileID).
		Scan(&mediaType, &version, &data); err != nil {
		return "", 0, nil, false
	}
	return mediaType, version, data, true
}

func (c *Client) storeContent(ctx context.Context, tileID, mediaType string, version int64, data []byte) {
	_, err := c.db.ExecContext(ctx, `INSERT INTO content (tile_id, media_type, version, data, fetched_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tile_id) DO UPDATE SET media_type=excluded.media_type, version=excluded.version, data=excluded.data, fetched_at=excluded.fetched_at`,
		tileID, mediaType, version, data, now())
	logErr("store content", err)
}

// ── Subscribe ───────────────────────────────────────────────────────────

// Subscribe passes the event stream through with a tee: the cache tracks
// the live session's mutations as the server relays them. If nothing is
// subscribed (headless node), reads still refresh on their own — the tee
// is an accelerator, not the correctness path.
func (c *Client) Subscribe(ctx context.Context, in *pb.SubscribeRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pb.Event], error) {
	stream, err := c.GridwellClient.Subscribe(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	// Every successful (re)subscription means the mount is up NOW — the
	// moment to warm the whole mount (issue #254): the initial connect
	// and each health-up reconnect land here, so the walk doubles as the
	// resync for grids nobody re-opened while the mount was dark.
	c.kickPrefetch()
	return &teeEventStream{ServerStreamingClient: stream, c: c, ctx: ctx}, nil
}

// applyEvent folds one mount event into the cache. GridChanged carries
// only an id — nothing to apply; the next successful GetGrid refreshes.
func (c *Client) applyEvent(ctx context.Context, ev *pb.Event) {
	switch p := ev.GetPayload().(type) {
	case *pb.Event_TileChanged:
		c.upsertTile(ctx, p.TileChanged.GetTile())
	case *pb.Event_TileRemoved:
		c.deleteTile(ctx, p.TileRemoved.GetTileId())
	}
}
