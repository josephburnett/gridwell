// Package sourcecache is the node's one memory of what a connection last
// answered: a read-through layer over <home>/cache.db, in front of the
// transport. Home is the durable store and a plugin is a subprocess a call
// away, so neither is fronted. Only what a connection itself said lives here,
// as marshaled protos keyed by the ids this layer sees, so there is no schema
// to drift against the contract. A grid read serves first and refreshes behind
// (GetGrid); every other read passes through and falls back to the remembered
// answer on a transport failure only, since an answered "gone" is never
// masked. An answer already stamped stale is never remembered, because it
// would overwrite the good answer it degraded from.
package sourcecache

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/dbformat"
	"github.com/josephburnett/gridwell/internal/namespace"
	_ "modernc.org/sqlite"
)

// cacheApplicationID stamps a source-cache file as ours, so a foreign SQLite
// file at the cache path is refused rather than overwritten. The bytes are
// frozen: re-stamping would refuse every existing cache.db for nothing.
const cacheApplicationID int64 = 0x67776d63

// cacheSchemaVersion is the current cache generation. The file is disposable,
// but versioning still refuses newer files and migrates older ones.
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
DROP INDEX IF EXISTS idx_mountcache_tiles_grid;
CREATE INDEX IF NOT EXISTS idx_sourcecache_tiles_grid ON tiles(grid_id);
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
-- The /content/ door's bounded body cache. Added without a version bump:
-- schemaDDL runs at every Open and the table is additive, which is the
-- liberty the disposable, non-frozen format buys.
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

// freshWindow is how long a remembered grid answers without a revalidation. It
// also keeps refresh from feeding on itself: the client's refetch lands inside
// the window of the revalidation that caused it.
const freshWindow = 30 * time.Second

// Options is the per-seam policy over the one engine: how eagerly it warms.
type Options struct {
	// Prefetch warms grids, tiles, previews and bodies nobody has opened yet;
	// prefetch.go owns both triggers. It belongs to a namespace whose absence
	// is a machine going dark, and defaults off.
	Prefetch bool
	// FreshWindow overrides freshWindow. Zero takes the default.
	FreshWindow time.Duration
}

// window is this layer's serve-first horizon.
func (c *Layer) window() time.Duration {
	if c.opts.FreshWindow > 0 {
		return c.opts.FreshWindow
	}
	return freshWindow
}

// Layer is the cache in front of one namespace. Every method not overridden
// here passes through the embedded Namespace untouched.
type Layer struct {
	namespace.Namespace
	db   *sql.DB
	opts Options
	pf   prefetcher

	// revalidations in flight, one per grid id, on pf.ctx so Close cancels
	// them; revalWG lets Close wait, so nothing writes into a closed DB.
	revalMu       sync.Mutex
	revalInflight map[string]bool
	revalWG       sync.WaitGroup

	// subs are this layer's own event subscribers, fed by revalidations that
	// changed or evicted a grid and by cache-store health transitions.
	subsMu sync.Mutex
	subs   map[int]chan *pb.Event
	subSeq int

	// cacheDown remembers that stores are failing, so the transition, not
	// every failure, surfaces as this namespace's health.
	healthMu  sync.Mutex
	cacheDown bool

	// dark is what this layer knows about reaching each source behind it, by
	// connection segment ("" for an upstream whose ids are unchained). A
	// remembered grid inside its window still stamps stale when its
	// connection is dark. setDark is the one writer.
	darkMu sync.Mutex
	dark   map[string]bool
}

// sourceOf names the source an id belongs to: the connection segment of a
// chained id. An unchained id belongs to the one unnamed source.
func sourceOf(id string) string {
	if first, _, ok := rpc.SplitID(id); ok {
		return first
	}
	return ""
}

// sourceOfNS names the source a namespace chain belongs to. A namespace is
// the chain, so a single segment names the connection rather than nothing.
func sourceOfNS(ns string) string {
	if first, _, ok := rpc.SplitID(ns); ok {
		return first
	}
	return ns
}

// setDark is the one writer of c.dark. A failed call and the source's own
// health are the same fact from two directions, so they take the same door.
// What they do not share is whether the client has to be told, which is
// announce, since only the caller knows whether anyone else saw this. The
// transition back to light is shared, and it is the prefetch walk's second
// trigger, because "this source is back" is one fact whichever direction
// notices first.
func (c *Layer) setDark(source string, dark bool, announce bool, grid func() string) {
	c.darkMu.Lock()
	changed := c.dark[source] != dark
	c.dark[source] = dark
	c.darkMu.Unlock()
	if !changed {
		return
	}
	if !dark {
		c.kickPrefetch(source)
	}
	if !announce || grid == nil {
		return
	}
	if id := grid(); id != "" {
		c.emitGridChanged(id)
	}
}

func (c *Layer) isDark(source string) bool {
	c.darkMu.Lock()
	defer c.darkMu.Unlock()
	return c.dark[source]
}

// noteReach records one pass-through outcome as this layer's reachability of
// the source the call named. A coded refusal is an answer, so only a transport
// failure is darkness. It announces, because nobody else watched the call.
func (c *Layer) noteReach(err error, source string, grid func() string) {
	c.setDark(source, err != nil && gwerr.IsTransport(err), true, grid)
}

// noteReachGrid and noteReachTile are noteReach for the two shapes of call.
// The tile's grid is looked up only on the transition, hence the closure.
func (c *Layer) noteReachGrid(err error, gridID string) {
	c.noteReach(err, sourceOf(gridID), func() string { return gridID })
}

func (c *Layer) noteReachTile(ctx context.Context, err error, tileID string) {
	c.noteReach(err, sourceOf(tileID), func() string {
		var gridID string
		if qerr := c.db.QueryRowContext(ctx, `SELECT grid_id FROM tiles WHERE id = ?`, tileID).Scan(&gridID); qerr != nil {
			return "" // nothing remembered names this tile: no grid to re-read
		}
		return gridID
	})
}

// The cache is a namespace in front of a namespace.
var _ namespace.Namespace = (*Layer)(nil)

// Store is the one cache file, shared by every layer over it. SQLite is
// single-writer per file, so a handle per namespace would only put each
// layer's write behind another handle's busy timeout.
type Store struct {
	db *sql.DB
	// down is why this store has no file at all, empty when it has one.
	down   string
	mu     sync.Mutex
	closed bool
	layers []*Layer
}

// Unavailable is the store for a node whose cache file could not be opened.
// It fronts pass-through and reports the missing cache as the namespace's
// health: losing serve-first and offline reading fails no read and shows
// nowhere, so a node that only logged it would look healthy for hours. The
// caller gets a Store either way, so the node has one cache path.
func Unavailable(detail string) *Store { return &Store{down: detail} }

// Open opens (or creates) the node's cache DB at dbPath.
func Open(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("sourcecache open %s: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("sourcecache pragmas: %w", err)
	}
	if _, err := db.Exec(schemaDDL); err != nil {
		db.Close()
		return nil, fmt.Errorf("sourcecache schema: %w", err)
	}
	if err := dbformat.EnsureVersion(context.Background(), db, cacheApplicationID, cacheSchemaVersion, nil); err != nil {
		db.Close()
		return nil, fmt.Errorf("sourcecache %s: %w", dbPath, err)
	}
	return &Store{db: db}, nil
}

// Front puts the cache in front of one namespace under the given policy: a
// Layer over an open file, and the pass-through below over a store with none.
// The store owns what it returns and shuts it down before the file goes away.
func (s *Store) Front(upstream namespace.Namespace, opts Options) namespace.Namespace {
	if s.down != "" {
		return &missing{Namespace: upstream, detail: s.down}
	}
	return s.front(upstream, opts)
}

// missing is the front over a store with no file. Every call passes through;
// the stream opens with the health, once per subscriber, because a cache that
// never opened has no transition to announce.
type missing struct {
	namespace.Namespace
	detail string
}

func (m *missing) Subscribe(ctx context.Context, in *pb.SubscribeRequest, send func(*pb.Event) error) error {
	if err := send(healthEvent(false, m.detail)); err != nil {
		return err
	}
	return m.Namespace.Subscribe(ctx, in, send)
}

func (s *Store) front(upstream namespace.Namespace, opts Options) *Layer {
	c := &Layer{Namespace: upstream, db: s.db, opts: opts,
		revalInflight: map[string]bool{}, subs: map[int]chan *pb.Event{}, dark: map[string]bool{}}
	c.pf.running = map[string]bool{}
	c.pf.ctx, c.pf.cancel = context.WithCancel(context.Background())
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed { // opened after the store closed: this layer never warms
		c.stopWalks()
		return c
	}
	s.layers = append(s.layers, c)
	return c
}

// Close stops every layer's walk and closes the file. Idempotent.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	layers := s.layers
	s.layers = nil
	s.mu.Unlock()
	for _, c := range layers {
		c.stopWalks()    // the walk is out before its DB goes away
		c.revalWG.Wait() // and so is every in-flight revalidation
	}
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// logErr is the log line for a cache-side failure. A broken cache never fails
// a live request, but under serve-first it is the read path, so failing to
// remember is user-visible degradation; noteCache surfaces it.
func logErr(op string, err error) {
	if err != nil {
		log.Printf("gridwell: sourcecache %s: %v (answers are not being remembered)", op, err)
	}
}

// noteCache records one cache-write outcome and surfaces the transitions as
// this namespace's health, never once per failure. A server log is not where
// the user looks.
func (c *Layer) noteCache(op string, err error) {
	logErr(op, err)
	c.healthMu.Lock()
	transition := (err != nil) != c.cacheDown
	c.cacheDown = err != nil
	c.healthMu.Unlock()
	if !transition {
		return
	}
	if err != nil {
		c.emitHealth(false, "the cache cannot remember answers ("+op+": "+err.Error()+"); every read now pays the source's full latency")
		return
	}
	c.emitHealth(true, "")
}

// healthEvent is a cache-side health report on the wire. The uuid rides empty;
// the fan-in fills it (see rpc.QualifyEventIDs).
func healthEvent(healthy bool, detail string) *pb.Event {
	return &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{
		Healthy: healthy, Detail: detail,
	}}}
}

// emitHealth announces a health transition to the synthetic stream's
// subscribers.
func (c *Layer) emitHealth(healthy bool, detail string) {
	ev := healthEvent(healthy, detail)
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	for _, ch := range c.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func now() int64 { return time.Now().Unix() }

func (c *Layer) Info(ctx context.Context, in *pb.InfoRequest) (*pb.InfoResponse, error) {
	resp, err := c.Namespace.Info(ctx, in)
	c.noteReach(err, "", nil) // an unnamed call: reachability, no grid to re-read
	if err == nil {
		if b, merr := proto.Marshal(resp); merr == nil {
			_, werr := c.db.ExecContext(ctx, `INSERT INTO info (k, proto) VALUES ('info', ?)
				ON CONFLICT(k) DO UPDATE SET proto=excluded.proto`, b)
			c.noteCache("store info", werr)
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

// Handshake forwards the routed plugin list and remembers the answer per
// namespace, so a remote pane's + menu is readable while the source is dark.
func (c *Layer) Handshake(ctx context.Context, in *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	resp, err := c.Namespace.Handshake(ctx, in)
	c.noteReach(err, sourceOfNS(in.GetNamespace()), nil)
	if err == nil {
		if b, merr := proto.Marshal(resp); merr == nil {
			_, werr := c.db.ExecContext(ctx, `INSERT INTO pluginlists (ns, proto) VALUES (?, ?)
				ON CONFLICT(ns) DO UPDATE SET proto=excluded.proto`, in.GetNamespace(), b)
			c.noteCache("store pluginlist", werr)
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

// GetGrid serves first and refreshes behind: within freshWindow and with the
// connection not known dark the remembered answer serves as-is, otherwise it
// serves stamped stale and kicks one background revalidation whose landing
// emits a GridChanged. Only a miss waits on the source.
func (c *Layer) GetGrid(ctx context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	if cached, fetchedAt, hit := c.loadGrid(ctx, in.GridId); hit {
		if time.Since(time.Unix(fetchedAt, 0)) < c.window() && !c.isDark(sourceOf(in.GridId)) {
			return cached, nil
		}
		// The stale bit is wire-only, never stored, so the revalidation
		// re-stores the grid without it.
		if cached.GetGrid() != nil {
			cached.Grid.Stale = true
		}
		c.revalidateGrid(in.GridId)
		return cached, nil
	}
	// A miss has nothing better than the source's word.
	return c.getGridLive(ctx, in.GridId)
}

// getGridLive reads one grid from the source and remembers the answer: the
// miss path, the revalidation, and the prefetch walk, which must never be
// answered by the rows it is warming. A stale answer is never remembered,
// because it would overwrite the good answer it degraded from with a poorer
// one that succeeds and that nothing but a live read would correct.
func (c *Layer) getGridLive(ctx context.Context, gridID string) (*pb.GetGridResponse, error) {
	resp, err := c.Namespace.GetGrid(ctx, &pb.GetGridRequest{GridId: gridID})
	c.noteReachGrid(err, gridID)
	if err != nil {
		return nil, err
	}
	if !resp.GetGrid().GetStale() {
		c.storeGrid(ctx, gridID, resp)
	}
	return resp, nil
}

// revalidateGrid refreshes one remembered grid in the background, single-flight
// per grid id, on the layer's own context so a canceled click never kills a
// refresh other readers want. A changed answer is stored and announced; a
// transport failure or a stale-stamped answer changes nothing; a verdict
// evicts, so the next read surfaces it instead of a remembered ghost.
func (c *Layer) revalidateGrid(gridID string) {
	c.revalMu.Lock()
	if c.revalInflight[gridID] {
		c.revalMu.Unlock()
		return
	}
	c.revalInflight[gridID] = true
	c.revalMu.Unlock()
	c.revalWG.Add(1)
	go func() {
		defer func() {
			c.revalMu.Lock()
			delete(c.revalInflight, gridID)
			c.revalMu.Unlock()
			c.revalWG.Done()
		}()
		ctx := c.pf.ctx
		old, _, hit := c.loadGrid(ctx, gridID)
		resp, err := c.getGridLive(ctx, gridID)
		switch {
		case err == nil && !resp.GetGrid().GetStale():
			if !hit || !gridRespEqual(old, resp) {
				c.emitGridChanged(gridID)
			}
		case err != nil && !gwerr.IsTransport(err):
			// An answered error is an answer: the remembered grid must not
			// outlive the source's verdict. A canceled ctx reads as transport
			// and evicts nothing.
			c.evictGrid(ctx, gridID)
			c.emitGridChanged(gridID)
		}
	}()
}

// gridRespEqual compares two grid answers, tiles by id so row order never
// fakes a change.
func gridRespEqual(a, b *pb.GetGridResponse) bool {
	if !proto.Equal(a.GetGrid(), b.GetGrid()) || len(a.GetTiles()) != len(b.GetTiles()) {
		return false
	}
	byID := make(map[string]*pb.Tile, len(a.GetTiles()))
	for _, t := range a.GetTiles() {
		byID[t.GetId()] = t
	}
	for _, t := range b.GetTiles() {
		if !proto.Equal(byID[t.GetId()], t) {
			return false
		}
	}
	return true
}

// evictGrid forgets a grid and its tile rows. Content and preview rows linger
// unreachable and refresh on their next live read.
func (c *Layer) evictGrid(ctx context.Context, gridID string) {
	_, err := c.db.ExecContext(ctx, `DELETE FROM tiles WHERE grid_id = ?`, gridID)
	c.noteCache("evict tiles", err)
	_, err = c.db.ExecContext(ctx, `DELETE FROM grids WHERE id = ?`, gridID)
	c.noteCache("evict grid", err)
}

// storeGrid replaces the grid row and its whole tile set in one transaction. A
// live GetGrid is the complete list, so this is also how deletions made while
// dark reconcile.
func (c *Layer) storeGrid(ctx context.Context, gridID string, resp *pb.GetGridResponse) {
	gb, err := proto.Marshal(resp.GetGrid())
	if err != nil {
		c.noteCache("marshal grid", err)
		return
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		c.noteCache("store grid", err)
		return
	}
	ts := now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO grids (id, proto, fetched_at) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET proto=excluded.proto, fetched_at=excluded.fetched_at`, gridID, gb, ts); err != nil {
		c.noteCache("store grid", err)
		_ = tx.Rollback()
		return
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tiles WHERE grid_id = ?`, gridID); err != nil {
		c.noteCache("store grid", err)
		_ = tx.Rollback()
		return
	}
	for _, t := range resp.GetTiles() {
		tb, merr := proto.Marshal(t)
		if merr != nil {
			c.noteCache("marshal tile", merr)
			_ = tx.Rollback()
			return
		}
		// Upsert, never a plain insert: after an id migration the same grid
		// answers under two keys and its tiles keep their ids, so a row may
		// already exist under the old grid key. The upsert moves it here.
		if _, err := tx.ExecContext(ctx, `INSERT INTO tiles (id, grid_id, proto, fetched_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET grid_id=excluded.grid_id, proto=excluded.proto, fetched_at=excluded.fetched_at`,
			t.GetId(), gridID, tb, ts); err != nil {
			c.noteCache("store tile", err)
			_ = tx.Rollback()
			return
		}
	}
	c.noteCache("store grid", tx.Commit())
}

func (c *Layer) loadGrid(ctx context.Context, gridID string) (resp *pb.GetGridResponse, fetchedAt int64, ok bool) {
	var gb []byte
	if err := c.db.QueryRowContext(ctx, `SELECT proto, fetched_at FROM grids WHERE id = ?`, gridID).Scan(&gb, &fetchedAt); err != nil {
		return nil, 0, false
	}
	g := &pb.Grid{}
	if err := proto.Unmarshal(gb, g); err != nil {
		return nil, 0, false
	}
	rows, err := c.db.QueryContext(ctx, `SELECT proto FROM tiles WHERE grid_id = ?`, gridID)
	if err != nil {
		return nil, 0, false
	}
	defer rows.Close()
	resp = &pb.GetGridResponse{Grid: g}
	for rows.Next() {
		var tb []byte
		if err := rows.Scan(&tb); err != nil {
			return nil, 0, false
		}
		t := &pb.Tile{}
		if err := proto.Unmarshal(tb, t); err != nil {
			return nil, 0, false
		}
		resp.Tiles = append(resp.Tiles, t)
	}
	return resp, fetchedAt, rows.Err() == nil
}

func (c *Layer) GetTile(ctx context.Context, in *pb.GetTileRequest) (*pb.TileResponse, error) {
	resp, err := c.Namespace.GetTile(ctx, in)
	c.noteReachTile(ctx, err, in.TileId)
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

func (c *Layer) upsertTile(ctx context.Context, t *pb.Tile) {
	if t == nil || t.GetId() == "" {
		return
	}
	tb, err := proto.Marshal(t)
	if err != nil {
		c.noteCache("marshal tile", err)
		return
	}
	_, werr := c.db.ExecContext(ctx, `INSERT INTO tiles (id, grid_id, proto, fetched_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET grid_id=excluded.grid_id, proto=excluded.proto, fetched_at=excluded.fetched_at`,
		t.GetId(), t.GetGridId(), tb, now())
	c.noteCache("upsert tile", werr)
}

func (c *Layer) deleteTile(ctx context.Context, tileID string) {
	_, err := c.db.ExecContext(ctx, `DELETE FROM tiles WHERE id = ?`, tileID)
	c.noteCache("delete tile", err)
	_, err = c.db.ExecContext(ctx, `DELETE FROM content WHERE tile_id = ?`, tileID)
	c.noteCache("delete content", err)
	_, err = c.db.ExecContext(ctx, `DELETE FROM previews WHERE tile_id = ?`, tileID)
	c.noteCache("delete preview", err)
}

func (c *Layer) GetTilePreview(ctx context.Context, in *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	resp, err := c.Namespace.GetTilePreview(ctx, in)
	c.noteReachTile(ctx, err, in.TileId)
	if err == nil {
		if jpeg := resp.GetJpeg(); len(jpeg) > 0 {
			_, werr := c.db.ExecContext(ctx, `INSERT INTO previews (tile_id, jpeg, fetched_at) VALUES (?, ?, ?)
				ON CONFLICT(tile_id) DO UPDATE SET jpeg=excluded.jpeg, fetched_at=excluded.fetched_at`,
				in.TileId, jpeg, now())
			c.noteCache("store preview", werr)
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

// ReadContent tees the live stream, storing only at a clean end, because a
// partial body served later would be silent corruption. A transport failure
// before any chunk falls back to the remembered body; after a chunk has
// flowed the error passes through, since splicing cache into a half-live
// stream would fabricate a body nobody ever had.
func (c *Layer) ReadContent(ctx context.Context, in *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	var mediaType string
	var version int64
	var data []byte
	var gotChunk, oversized bool
	err := c.Namespace.ReadContent(ctx, in, func(ch *pb.ContentChunk) error {
		gotChunk = true
		// Chunk 1 carries media_type and version, sent even for empty content.
		if mediaType == "" && ch.GetMediaType() != "" {
			mediaType = ch.GetMediaType()
		}
		if version == 0 && ch.GetVersion() != 0 {
			version = ch.GetVersion()
		}
		if !oversized {
			data = append(data, ch.GetData()...)
			if len(data) > rpc.MaxContentBytes {
				oversized = true
				data = nil
			}
		}
		return send(ch)
	})
	c.noteReachTile(ctx, err, in.TileId)
	if err == nil {
		if !oversized {
			c.storeContent(ctx, in.TileId, mediaType, version, data)
		}
		return nil
	}
	if !gotChunk && gwerr.IsTransport(err) {
		if mt, ver, cached, ok := c.loadContent(ctx, in.TileId); ok {
			return sendChunked(cached, func(b []byte, first bool) error {
				if first {
					return send(&pb.ContentChunk{MediaType: mt, Version: ver, Data: b})
				}
				return send(&pb.ContentChunk{Data: b})
			})
		}
	}
	return err
}

// sendChunked replays a remembered body in the live chunk shape, so a caller
// cannot tell a remembered answer from a live one by its framing. One empty
// first chunk still goes out for an empty body, carrying the metadata.
func sendChunked(data []byte, emit func(b []byte, first bool) error) error {
	first := true
	for {
		n := min(len(data), rpc.ContentChunkBytes)
		if err := emit(data[:n], first); err != nil {
			return err
		}
		data = data[n:]
		first = false
		if len(data) == 0 {
			return nil
		}
	}
}

func (c *Layer) loadContent(ctx context.Context, tileID string) (mediaType string, version int64, data []byte, ok bool) {
	if err := c.db.QueryRowContext(ctx, `SELECT media_type, version, data FROM content WHERE tile_id = ?`, tileID).
		Scan(&mediaType, &version, &data); err != nil {
		return "", 0, nil, false
	}
	return mediaType, version, data, true
}

func (c *Layer) storeContent(ctx context.Context, tileID, mediaType string, version int64, data []byte) {
	_, err := c.db.ExecContext(ctx, `INSERT INTO content (tile_id, media_type, version, data, fetched_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tile_id) DO UPDATE SET media_type=excluded.media_type, version=excluded.version, data=excluded.data, fetched_at=excluded.fetched_at`,
		tileID, mediaType, version, blob(data), now())
	c.noteCache("store content", err)
}

// blob binds a body for one of the NOT NULL blob columns. A nil []byte would
// bind as SQL NULL, and an empty answer is an answer the cache must remember.
// Both body writers go through here, because the fault is the binding.
func blob(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// Subscribe serves two streams as one: the upstream's, teed so the cache
// tracks the live session's mutations, and the layer's own. Both always: the
// layer's stream closes the serve-first loop, which nothing upstream can know
// about, and the tee is only an accelerator.
func (c *Layer) Subscribe(ctx context.Context, in *pb.SubscribeRequest, send func(*pb.Event) error) error {
	// Every subscription and resubscription is a moment to warm every source.
	// One connection going dark and coming back does not land here, because
	// the stream this relays is the transport's hub, which survives it; that
	// source is warmed when setDark sees it come back.
	c.kickPrefetch("")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Two goroutines relay the two streams; the caller's send is not
	// concurrent.
	var sendMu sync.Mutex
	emit := func(ev *pb.Event) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return send(ev)
	}
	id, ch := c.addSub()
	defer c.removeSub(id)
	var ownErr error
	own := make(chan struct{})
	go func() {
		defer close(own)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-ch:
				if err := emit(ev); err != nil {
					ownErr = err
					cancel()
					return
				}
			}
		}
	}()
	err := c.Namespace.Subscribe(ctx, in, func(ev *pb.Event) error {
		c.applyEvent(ctx, ev)
		return emit(ev)
	})
	if status.Code(err) == codes.Unimplemented {
		// An upstream with no stream of its own: ours stands alone until the
		// subscriber goes away.
		<-ctx.Done()
		err = nil
	}
	cancel()
	<-own
	if err != nil {
		return err
	}
	return ownErr
}

// addSub registers one synthetic-stream subscriber.
func (c *Layer) addSub() (int, chan *pb.Event) {
	ch := make(chan *pb.Event, 64)
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	c.subSeq++
	c.subs[c.subSeq] = ch
	return c.subSeq, ch
}

func (c *Layer) removeSub(id int) {
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	delete(c.subs, id)
}

// emitGridChanged announces one changed or evicted grid to the synthetic
// stream's subscribers, under this layer's local id. A subscriber too far
// behind loses the event rather than blocking a revalidation: the rows are
// stored, so it is a missed refresh, not a missed fact.
func (c *Layer) emitGridChanged(gridID string) {
	ev := &pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: gridID}}}
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	for _, ch := range c.subs {
		select {
		case ch <- ev:
		default:
			log.Printf("gridwell: sourcecache: a subscriber missed GridChanged %s (buffer full)", gridID)
		}
	}
}

// applyEvent folds one event into the cache. GridChanged carries only an id,
// so there is nothing to apply and the next GetGrid refreshes.
func (c *Layer) applyEvent(ctx context.Context, ev *pb.Event) {
	switch p := ev.GetPayload().(type) {
	case *pb.Event_TileChanged:
		c.upsertTile(ctx, p.TileChanged.GetTile())
	case *pb.Event_TileRemoved:
		c.deleteTile(ctx, p.TileRemoved.GetTileId())
	case *pb.Event_PluginHealth:
		// The source's own supervisor says whether it can be reached, so a
		// room re-entered after a machine died says it is a memory without
		// waiting for a call of this layer's own to fail. A key deeper than a
		// connection segment is its own, since a far plugin being down does
		// not make the machine unreachable. It does not announce: the event is
		// relayed onward to the very client that would be told.
		c.setDark(p.PluginHealth.GetPluginUuid(), !p.PluginHealth.GetHealthy(), false, nil)
	}
}

// MintRef passes the mint through to the source. It is a write, so there is
// nothing to serve when the source is dark: a reference that cannot be minted
// must not be stored.
func (l *Layer) MintRef(ctx context.Context, localID string) (string, error) {
	return namespace.MintRef(ctx, l.Namespace, localID)
}

// Writes pass through untouched, so the source stays the one owner of its
// truth, and a successful response updates the remembered rows by the same
// fold the event tee applies, which keeps a moved tile from snapping back.

// foldWrite folds one in-place write's answer into the remembered rows. A
// write against a derived tile mints its row and the answer renames it, so the
// row under the requested id must go or the listing reads a ghost twin. Only
// for verbs that mutate the tile they name.
func (c *Layer) foldWrite(ctx context.Context, reqTileID string, t *pb.Tile) {
	if t.GetId() != "" && reqTileID != "" && reqTileID != t.GetId() {
		c.deleteTile(ctx, reqTileID)
	}
	c.upsertTile(ctx, t)
}

func (c *Layer) CreateTile(ctx context.Context, in *pb.CreateTileRequest) (*pb.TileResponse, error) {
	resp, err := c.Namespace.CreateTile(ctx, in)
	c.noteReachGrid(err, in.GridId)
	if err == nil {
		c.upsertTile(ctx, resp.GetTile())
	}
	return resp, err
}

func (c *Layer) SetTile(ctx context.Context, in *pb.SetTileRequest) (*pb.TileResponse, error) {
	resp, err := c.Namespace.SetTile(ctx, in)
	c.noteReachTile(ctx, err, in.TileId)
	if err == nil {
		c.foldWrite(ctx, in.TileId, resp.GetTile())
	}
	return resp, err
}

func (c *Layer) PlaceTile(ctx context.Context, in *pb.PlaceTileRequest) (*pb.TileResponse, error) {
	resp, err := c.Namespace.PlaceTile(ctx, in)
	c.noteReachTile(ctx, err, in.TileId)
	if err == nil {
		c.foldWrite(ctx, in.TileId, resp.GetTile())
	}
	return resp, err
}

func (c *Layer) CloneTile(ctx context.Context, in *pb.CloneTileRequest) (*pb.TileResponse, error) {
	resp, err := c.Namespace.CloneTile(ctx, in)
	c.noteReachTile(ctx, err, in.TileId)
	if err == nil {
		c.upsertTile(ctx, resp.GetTile()) // the request names the source, which stays
	}
	return resp, err
}

func (c *Layer) SetFraming(ctx context.Context, in *pb.SetFramingRequest) (*pb.SetFramingResponse, error) {
	resp, err := c.Namespace.SetFraming(ctx, in)
	c.noteReachTile(ctx, err, in.TileId)
	if err == nil && resp.GetTile() != nil { // nil for a root-grid framing
		c.foldWrite(ctx, in.TileId, resp.GetTile())
	}
	return resp, err
}

func (c *Layer) DeleteTile(ctx context.Context, in *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	resp, err := c.Namespace.DeleteTile(ctx, in)
	c.noteReachTile(ctx, err, in.TileId)
	if err == nil {
		c.deleteTile(ctx, in.TileId)
	}
	return resp, err
}

// WriteContent forwards the stream and, on the commit, drops the remembered
// body rather than guessing at it: a remembered body must be one the source
// vouched for whole. The tile row is updated because the version moved.
func (c *Layer) WriteContent(ctx context.Context, recv func() (*pb.WriteContentRequest, error)) (*pb.TileResponse, error) {
	var tileID string
	resp, err := c.Namespace.WriteContent(ctx, func() (*pb.WriteContentRequest, error) {
		req, rerr := recv()
		if rerr == nil && tileID == "" {
			tileID = req.GetTileId()
		}
		return req, rerr
	})
	c.noteReachTile(ctx, err, tileID)
	if err == nil {
		if tileID != "" {
			_, derr := c.db.ExecContext(ctx, `DELETE FROM content WHERE tile_id = ?`, tileID)
			c.noteCache("drop written content", derr)
		}
		c.foldWrite(ctx, tileID, resp.GetTile())
	}
	return resp, err
}
