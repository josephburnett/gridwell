// Package sourcecache is the node's one memory of what a connection last
// answered: a read-through layer over <home>/cache.db in front of the
// transport, holding marshaled protos keyed by id. A grid read serves first
// and refreshes behind (GetGrid); every other read falls back to the
// remembered answer on a transport failure only, so an answered "gone" is
// never masked.
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
	"github.com/josephburnett/gridwell/internal/eventhub"
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
DROP TABLE IF EXISTS info;
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
	// Prefetch warms what nobody has opened yet (prefetch.go); off by default.
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

	// hub fans this layer's own stream out, fed by revalidations that changed
	// or evicted a grid and by cache-store health transitions.
	hub *eventhub.Hub[*pb.Event]

	// cacheDown remembers that stores are failing, so the transition, not
	// every failure, surfaces as this namespace's health.
	healthMu  sync.Mutex
	cacheDown bool

	// dark is reachability per connection segment ("" for unchained ids).
	// setDark is the one writer.
	darkMu sync.Mutex
	dark   map[string]bool

	order foldOrder
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

// setDark is the one writer of c.dark, for a failed call and the source's own
// health alike; announce is whether the client must be told. The transition
// back to light is the prefetch walk's second trigger.
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
// failure is darkness, and an abandoned call is nothing. It announces, because
// nobody else watched the call.
func (c *Layer) noteReach(ctx context.Context, err error, source string, grid func() string) {
	if gwerr.IsAbandoned(ctx, err) {
		return
	}
	c.setDark(source, err != nil && gwerr.IsTransport(err), true, grid)
}

// noteReachGrid and noteReachTile are noteReach for the two shapes of call.
// The tile's grid is looked up only on the transition, hence the closure.
func (c *Layer) noteReachGrid(ctx context.Context, err error, gridID string) {
	c.noteReach(ctx, err, sourceOf(gridID), func() string { return gridID })
}

func (c *Layer) noteReachTile(ctx context.Context, err error, tileID string) {
	c.noteReach(ctx, err, sourceOf(tileID), func() string {
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

// Unavailable is the store for a node whose cache file could not be opened: it
// passes through and reports the missing cache as the namespace's health,
// since a degradation that only logged would look healthy for hours.
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
	if err := send(rpc.HealthEvent("", false, m.detail)); err != nil {
		return err
	}
	return m.Namespace.Subscribe(ctx, in, send)
}

func (s *Store) front(upstream namespace.Namespace, opts Options) *Layer {
	c := &Layer{Namespace: upstream, db: s.db, opts: opts,
		revalInflight: map[string]bool{}, hub: eventhub.New(rpc.EventKey), dark: map[string]bool{}}
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

// emitHealth announces a health transition to the synthetic stream's
// subscribers.
func (c *Layer) emitHealth(healthy bool, detail string) {
	c.hub.Publish(rpc.HealthEvent("", healthy, detail))
}

func now() int64 { return time.Now().Unix() }

// Handshake forwards the routed plugin list and remembers it per namespace. A
// doorway answered with no framing keeps the remembered one (keepFraming).
func (c *Layer) Handshake(ctx context.Context, in *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	r := c.order.begin()
	defer r.end()
	resp, err := c.Namespace.Handshake(ctx, in)
	c.noteReach(ctx, err, sourceOfNS(in.GetNamespace()), nil)
	if err == nil {
		r.install(doorwayGrids(resp), func() {
			if old, ok := c.loadPluginList(ctx, in.GetNamespace()); ok {
				keepFraming(resp, old)
			}
			if b, merr := proto.Marshal(resp); merr == nil {
				_, werr := c.db.ExecContext(ctx, `INSERT INTO pluginlists (ns, proto) VALUES (?, ?)
					ON CONFLICT(ns) DO UPDATE SET proto=excluded.proto`, in.GetNamespace(), b)
				c.noteCache("store pluginlist", werr)
			}
		})
		return resp, nil
	}
	if !gwerr.IsTransport(err) {
		return nil, err
	}
	cached, ok := c.loadPluginList(ctx, in.GetNamespace())
	if !ok {
		return nil, err
	}
	return cached, nil
}

func (c *Layer) loadPluginList(ctx context.Context, ns string) (*pb.HandshakeResponse, bool) {
	var b []byte
	if err := c.db.QueryRowContext(ctx, `SELECT proto FROM pluginlists WHERE ns = ?`, ns).Scan(&b); err != nil {
		return nil, false
	}
	l := &pb.HandshakeResponse{}
	if err := proto.Unmarshal(b, l); err != nil {
		return nil, false
	}
	return l, true
}

// doorwayGrids names every grid a plugin list frames, the keys reframe folds
// under.
func doorwayGrids(l *pb.HandshakeResponse) []string {
	var ids []string
	for _, pl := range l.GetPlugins() {
		ids = append(ids, pl.GetRootGridId())
		for _, e := range pl.GetMenuEntries() {
			ids = append(ids, e.GetGridId())
		}
	}
	return ids
}

// keepFraming gives every doorway of fresh that answers no framing (an
// rpc.View with none) the framing old remembers for the same grid.
func keepFraming(fresh, old *pb.HandshakeResponse) {
	known := map[string]rpc.View{}
	remember := func(gridID string, v rpc.View) {
		if _, ok := v.Framing(); ok {
			known[gridID] = v
		}
	}
	for _, pl := range old.GetPlugins() {
		remember(pl.GetRootGridId(), rpc.ViewOf(pl.GetRootViewCx(), pl.GetRootViewCy(), pl.GetRootViewZoom()))
		for _, e := range pl.GetMenuEntries() {
			remember(e.GetGridId(), rpc.ViewOf(e.GetViewCx(), e.GetViewCy(), e.GetViewZoom()))
		}
	}
	unset := func(cx, cy, zoom float64) bool { return rpc.ViewOf(cx, cy, zoom).SameAs(rpc.View{}) }
	for _, pl := range fresh.GetPlugins() {
		if v, ok := known[pl.GetRootGridId()]; ok && unset(pl.GetRootViewCx(), pl.GetRootViewCy(), pl.GetRootViewZoom()) {
			pl.RootViewCx, pl.RootViewCy, pl.RootViewZoom = v.Wire()
		}
		for _, e := range pl.GetMenuEntries() {
			if v, ok := known[e.GetGridId()]; ok && unset(e.GetViewCx(), e.GetViewCy(), e.GetViewZoom()) {
				e.ViewCx, e.ViewCy, e.ViewZoom = v.Wire()
			}
		}
	}
}

// GetGrid serves first and refreshes behind: past freshWindow or with the
// connection dark, one background revalidation runs and announces a change.
// Only a miss waits on the source.
func (c *Layer) GetGrid(ctx context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	if cached, fetchedAt, hit := c.loadGrid(ctx, in.GridId); hit {
		if time.Since(time.Unix(fetchedAt, 0)) >= c.window() || c.isDark(sourceOf(in.GridId)) {
			c.revalidateGrid(in.GridId)
		}
		return cached, nil
	}
	// A miss has nothing better than the source's word.
	resp, _, err := c.getGridLive(ctx, in.GridId)
	return resp, err
}

// getGridLive reads one grid from the source and remembers the answer unless a
// fold overtook it (foldOrder); installed says which. It is the miss path, the
// revalidation, and the prefetch walk, which must never be answered by the
// rows it is warming.
func (c *Layer) getGridLive(ctx context.Context, gridID string) (resp *pb.GetGridResponse, installed bool, err error) {
	r := c.order.begin()
	defer r.end()
	resp, err = c.Namespace.GetGrid(ctx, &pb.GetGridRequest{GridId: gridID})
	c.noteReachGrid(ctx, err, gridID)
	if err != nil {
		return nil, false, err
	}
	keys := []string{gridID}
	for _, t := range resp.GetTiles() {
		keys = append(keys, t.GetId())
	}
	installed = r.install(keys, func() { c.storeGrid(ctx, gridID, resp) })
	return resp, installed, nil
}

// revalidateGrid refreshes one remembered grid in the background, single-flight
// per grid id on the layer's context. A change is stored and announced; an
// answer a fold overtook, or a transport failure, changes nothing; a verdict
// evicts.
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
		resp, installed, err := c.getGridLive(ctx, gridID)
		switch {
		case err == nil:
			if installed && (!hit || !gridRespEqual(old, resp)) {
				c.emitGridChanged(gridID)
			}
		case !gwerr.IsAbandoned(ctx, err) && !gwerr.IsTransport(err):
			// An answered error is an answer: the remembered grid must not
			// outlive the source's verdict.
			c.evictGrid(ctx, gridID)
			c.emitGridChanged(gridID)
		}
	}()
}

// gridRespEqual compares two grid answers, tiles by id. The grid's version is
// left out: no reader acts on it alone.
func gridRespEqual(a, b *pb.GetGridResponse) bool {
	if !proto.Equal(unversioned(a.GetGrid()), unversioned(b.GetGrid())) || len(a.GetTiles()) != len(b.GetTiles()) {
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

func unversioned(g *pb.Grid) *pb.Grid {
	g = proto.CloneOf(g)
	if g != nil {
		g.Version = 0
	}
	return g
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
	r := c.order.begin()
	defer r.end()
	resp, err := c.Namespace.GetTile(ctx, in)
	c.noteReachTile(ctx, err, in.TileId)
	if err == nil {
		t := resp.GetTile()
		r.install([]string{in.TileId, t.GetId()}, func() { c.upsertTile(ctx, t) })
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

// foldTile is the fold of one tile the source vouched for, by event or by a
// write's answer; upsertTile is the bare row write.
func (c *Layer) foldTile(ctx context.Context, t *pb.Tile) {
	c.order.fold([]string{t.GetId(), t.GetGridId()}, func() { c.upsertTile(ctx, t) })
}

func (c *Layer) deleteTile(ctx context.Context, tileID string) {
	c.order.fold([]string{tileID}, func() {
		_, err := c.db.ExecContext(ctx, `DELETE FROM tiles WHERE id = ?`, tileID)
		c.noteCache("delete tile", err)
		_, err = c.db.ExecContext(ctx, `DELETE FROM content WHERE tile_id = ?`, tileID)
		c.noteCache("delete content", err)
		_, err = c.db.ExecContext(ctx, `DELETE FROM previews WHERE tile_id = ?`, tileID)
		c.noteCache("delete preview", err)
	})
}

func (c *Layer) GetTilePreview(ctx context.Context, in *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	r := c.order.begin()
	defer r.end()
	resp, err := c.Namespace.GetTilePreview(ctx, in)
	c.noteReachTile(ctx, err, in.TileId)
	if err == nil {
		if jpeg := resp.GetJpeg(); len(jpeg) > 0 {
			r.install([]string{in.TileId}, func() {
				_, werr := c.db.ExecContext(ctx, `INSERT INTO previews (tile_id, jpeg, fetched_at) VALUES (?, ?, ?)
					ON CONFLICT(tile_id) DO UPDATE SET jpeg=excluded.jpeg, fetched_at=excluded.fetched_at`,
					in.TileId, jpeg, now())
				c.noteCache("store preview", werr)
			})
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

// ReadContent tees the live stream, storing only at a clean end. A transport
// failure falls back to the remembered body only before any chunk has flowed.
func (c *Layer) ReadContent(ctx context.Context, in *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	var mediaType string
	var version int64
	var data []byte
	var gotChunk, oversized bool
	r := c.order.begin()
	defer r.end()
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
			r.install([]string{in.TileId}, func() { c.storeContent(ctx, in.TileId, mediaType, version, data) })
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

// Subscribe serves the upstream's stream, teed into the cache, and the
// layer's own, which closes the serve-first loop.
func (c *Layer) Subscribe(ctx context.Context, in *pb.SubscribeRequest, send func(*pb.Event) error) error {
	// Every (re)subscription warms every source; one connection coming back
	// is setDark's trigger instead.
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
	ch, detach := c.hub.Subscribe()
	defer detach()
	var ownErr error
	own := make(chan struct{})
	go func() {
		defer close(own)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
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

// emitGridChanged announces one changed or evicted grid to the synthetic
// stream's subscribers, under this layer's local id.
func (c *Layer) emitGridChanged(gridID string) {
	c.hub.Publish(&pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: gridID}}})
}

// applyEvent folds one event into the cache. GridChanged carries only an id,
// so there is nothing to apply and the next GetGrid refreshes.
func (c *Layer) applyEvent(ctx context.Context, ev *pb.Event) {
	switch p := ev.GetPayload().(type) {
	case *pb.Event_TileChanged:
		c.foldTile(ctx, p.TileChanged.GetTile())
	case *pb.Event_TileRemoved:
		c.deleteTile(ctx, p.TileRemoved.GetTileId())
	case *pb.Event_GridFramingChanged:
		fc := p.GridFramingChanged
		if f, ok := rpc.ViewOf(fc.GetViewCx(), fc.GetViewCy(), fc.GetViewZoom()).Framing(); ok {
			c.reframe(ctx, fc.GetGridId(), f)
		}
	case *pb.Event_PluginHealth:
		// The source's supervisor says whether it can be reached. A key deeper
		// than a connection segment is a far plugin, not the machine. No
		// announce: the event itself is relayed onward.
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

// foldWrite folds one in-place write's answer into the remembered rows,
// dropping the row under the requested id when the answer renamed it.
func (c *Layer) foldWrite(ctx context.Context, reqTileID string, t *pb.Tile) {
	if t.GetId() != "" && reqTileID != "" && reqTileID != t.GetId() {
		c.deleteTile(ctx, reqTileID)
	}
	c.foldTile(ctx, t)
}

func (c *Layer) CreateTile(ctx context.Context, in *pb.CreateTileRequest) (*pb.TileResponse, error) {
	resp, err := c.Namespace.CreateTile(ctx, in)
	c.noteReachGrid(ctx, err, in.GridId)
	if err == nil {
		c.foldTile(ctx, resp.GetTile())
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
		c.foldTile(ctx, resp.GetTile()) // the request names the source, which stays
	}
	return resp, err
}

func (c *Layer) SetFraming(ctx context.Context, in *pb.SetFramingRequest) (*pb.SetFramingResponse, error) {
	resp, err := c.Namespace.SetFraming(ctx, in)
	c.noteReachTile(ctx, err, in.TileId)
	switch {
	case err != nil:
	case in.RootGridId != "":
		// The source accepted it, so it decodes.
		if f, ferr := rpc.FramingOf(in); ferr == nil {
			c.reframe(ctx, in.RootGridId, f)
		}
	default:
		c.foldWrite(ctx, in.TileId, resp.GetTile())
	}
	return resp, err
}

// reframe is the one writer of a root grid's remembered framing: it lands on
// every remembered doorway rooted there (rpc.Reframe), from the source's event
// and from a write the source accepted alike, which carry the same numbers.
func (c *Layer) reframe(ctx context.Context, gridID string, f rpc.Framing) {
	c.order.fold([]string{gridID}, func() { c.reframeRows(ctx, gridID, f) })
}

func (c *Layer) reframeRows(ctx context.Context, gridID string, f rpc.Framing) {
	rows, err := c.db.QueryContext(ctx, `SELECT ns, proto FROM pluginlists`)
	if err != nil {
		c.noteCache("load pluginlists", err)
		return
	}
	moved := map[string][]byte{}
	for rows.Next() {
		var ns string
		var b []byte
		if err := rows.Scan(&ns, &b); err != nil {
			_ = rows.Close()
			c.noteCache("load pluginlists", err)
			return
		}
		l := &pb.HandshakeResponse{}
		if proto.Unmarshal(b, l) != nil || !rpc.Reframe(gridID, f, l.Plugins) {
			continue
		}
		if nb, merr := proto.Marshal(l); merr == nil {
			moved[ns] = nb
		}
	}
	_ = rows.Close()
	for ns, b := range moved {
		_, werr := c.db.ExecContext(ctx, `UPDATE pluginlists SET proto = ? WHERE ns = ?`, b, ns)
		c.noteCache("reframe pluginlist", werr)
	}
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
			c.order.fold([]string{tileID}, func() {
				_, derr := c.db.ExecContext(ctx, `DELETE FROM content WHERE tile_id = ?`, tileID)
				c.noteCache("drop written content", derr)
			})
		}
		c.foldWrite(ctx, tileID, resp.GetTile())
	}
	return resp, err
}
