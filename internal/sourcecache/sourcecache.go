// Package sourcecache is the node's one memory of what a connection last
// answered. It sits in front of the transport — the one seam whose answers cross
// a network — as a read-through layer over one disposable file. Home is the
// durable store and a plugin is a subprocess a call away; neither is fronted,
// because a cache earns its keep across a network and nowhere else.
//
// A grid read serves first and refreshes behind. A remembered grid answers
// immediately, and the stale bit says whether that answer is a memory:
// unstamped within freshWindow of the answer it remembers, stamped beyond it,
// and stamped inside it too once the connection it came from is known dark,
// because then a memory is all it can be. A stamped read kicks one background
// revalidation per grid.
// A revalidation that lands a different answer replaces the rows and emits a
// GridChanged through the layer's own event stream (Subscribe below), so the
// client refetches and the correction reaches the screen without the source's
// latency — a gitlab walk, a remote round trip — ever sitting on the read
// path. A verdict from the revalidation — NotFound, PermissionDenied — evicts
// the remembered grid and emits the same event, so the next read passes
// through and the verdict surfaces rather than a ghost answering forever.
// Only a grid the cache has never seen still waits on the source.
//
// Every other read passes through and remembers; when the source is
// unreachable, on a transport-class failure only, since an answered "gone" is
// never masked, it serves the remembered answer. Every pass-through call is
// also how the layer learns whether a connection can be reached at all, and
// the connection's own health, riding the stream this layer relays, says the
// same thing from the other side; either way the discovery announces the grid
// at hand, so a client holding it re-reads and sees the stamp.
//
// Writes always pass through — the cache is never a write buffer, the source
// stays the one owner of its truth — and a write's successful response
// updates the remembered rows, because under serve-first "the next read
// refreshes" no longer holds and a moved tile must not snap back to its
// remembered place.
//
// It is a cache, not memory. The node's own facts — the ids it minted, where
// the user put them, how they are framed — are durable rows in gridwell.db.
// What lives here is only what a connection itself said: the handshake, the
// tile facts of a remote grid, bodies, previews, page bytes.
//
// Storage is one SQLite DB for the whole node, <home>/cache.db, and it is
// explicitly disposable: deleting it is always safe, since it re-warms from
// use; it is not backed up; and it is not under the frozen-format promise,
// being dbformat-versioned only so a future shape change can migrate or
// refuse cleanly. Rows are wire-shaped — marshaled protos keyed by the ids
// this layer sees — so there is no schema to drift against the contract. Ids
// never collide across namespaces: a local namespace's ids come from the
// store's one AUTOINCREMENT, and a connection's are qualified by its name
// segment.
//
// Freshness: every successful read replaces its rows, and the Subscribe
// stream the server already holds through this layer is teed, so a
// TileChanged upserts and a TileRemoved deletes and the cache tracks the live
// session without a poller. Deletions that happen while the source is dark
// are caught by the next successful GetGrid, which replaces the grid's whole
// tile set, so that read-through refresh is the resync by construction rather
// than by a sweep.
//
// Deliberately not cached: any answer already stamped stale, since
// remembering a degraded answer would overwrite the good one it degraded
// from. ServeContent bodies are cached under their own bounds
// (servecontent.go); the whole-source prefetch walk is a per-seam policy, off
// by default and opted into by the transport alone (prefetch.go).
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

// contentChunkBytes mirrors the plugins' ReadContent chunking so a
// cache-served stream is shaped like a live one.
const contentChunkBytes = 256 * 1024

// maxCachedContentBytes bounds one cached body. It matches the store's blob
// cap, so anything a plugin can serve as tile content fits; a larger stream
// still passes through live, just uncached.
const maxCachedContentBytes = 16 * 1024 * 1024

// freshWindow is how long a remembered grid answers without a revalidation:
// within it a hit serves unstamped and touches nothing, beyond it the hit
// serves stamped stale and kicks one background refresh. The window is also
// what keeps refresh from feeding on itself — a GridChanged makes the client
// refetch, and the refetch lands inside the window of the revalidation that
// caused it, so a source whose listing drifts on every walk settles into at
// most one refresh per window instead of a loop.
const freshWindow = 30 * time.Second

// Options is the per-seam policy over the one engine: what differs between two
// fronted namespaces is how eagerly the engine warms.
type Options struct {
	// Prefetch walks the whole namespace on every successful Subscribe,
	// warming grids, tiles, previews, and bodies nobody has opened yet; see
	// prefetch.go. It is the offline policy, and belongs to a namespace whose
	// answers cross a network and whose absence is a machine going dark: the
	// transport, which is the one namespace fronted today. The default is off,
	// so a seam fronted without asking for a crawl reads through and remembers,
	// nothing more.
	Prefetch bool
	// FreshWindow overrides freshWindow, the serve-first horizon. Zero takes
	// the default; tests shrink it so an aged answer is one they just stored.
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
// here passes through the embedded Namespace untouched: shells, Probe, and
// Search.
type Layer struct {
	namespace.Namespace
	db   *sql.DB
	opts Options
	pf   prefetcher

	// revalidations in flight, one per grid id, on pf.ctx so Close cancels
	// them; revalWG lets Close wait so a landing walk never writes into a
	// closed DB.
	revalMu       sync.Mutex
	revalInflight map[string]bool
	revalWG       sync.WaitGroup

	// subs are this layer's own event subscribers — the stream Subscribe
	// serves alongside the upstream's — fed by revalidations that changed or
	// evicted a grid, and by cache-store health transitions.
	subsMu sync.Mutex
	subs   map[int]chan *pb.Event
	subSeq int

	// cacheDown remembers that stores are failing, so the transition — not
	// every failure — surfaces as this namespace's health.
	healthMu  sync.Mutex
	cacheDown bool

	// dark is what this layer knows about reaching each source behind it, by
	// connection segment ("" for an upstream whose ids are unchained). It is
	// the second half of "this serve is a memory": a remembered grid is
	// stamped past its window because nothing has confirmed it, and stamped
	// inside its window when the connection it came from is known dark. Two
	// things write it, and they are the same fact from two directions — a
	// pass-through call that failed transport-shaped, and the connection's
	// own health on the stream this layer already relays.
	darkMu sync.Mutex
	dark   map[string]bool
}

// sourceOf names the source an id belongs to: the connection segment of a
// chained id. An unchained id — an upstream that is not the transport —
// belongs to the one unnamed source, which is the whole layer.
func sourceOf(id string) string {
	if first, _, ok := rpc.SplitID(id); ok {
		return first
	}
	return ""
}

// sourceOfNS names the source a namespace chain belongs to. A namespace IS
// the chain, so a single segment names the connection rather than nothing.
func sourceOfNS(ns string) string {
	if first, _, ok := rpc.SplitID(ns); ok {
		return first
	}
	return ns
}

// setDark records one source's reachability and reports whether that changed
// it. Idempotent: only the transition is news.
func (c *Layer) setDark(source string, dark bool) bool {
	c.darkMu.Lock()
	defer c.darkMu.Unlock()
	if c.dark[source] == dark {
		return false
	}
	c.dark[source] = dark
	return true
}

func (c *Layer) isDark(source string) bool {
	c.darkMu.Lock()
	defer c.darkMu.Unlock()
	return c.dark[source]
}

// noteReach records one pass-through outcome as this layer's reachability of
// the source the call named, and, when that changes, announces the grid at
// hand so a client already holding it re-reads: dark, it wants the stamp and
// the cached chip; light again, it wants the live answer back. A coded
// refusal is an answer, so the source is reachable — only a transport-shaped
// failure is darkness. grid is consulted on the transition alone.
func (c *Layer) noteReach(err error, source string, grid func() string) {
	if !c.setDark(source, err != nil && gwerr.IsTransport(err)) || grid == nil {
		return
	}
	if id := grid(); id != "" {
		c.emitGridChanged(id)
	}
}

// noteReachGrid and noteReachTile are noteReach for the two shapes of call:
// one names a grid, the other a tile whose grid the cache can look up (only
// on the transition, which is why it is a closure).
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

// The cache is a namespace in front of a namespace, nothing more.
var _ namespace.Namespace = (*Layer)(nil)

// Store is the one cache file, shared by every layer over it. SQLite is
// single-writer per file, so a handle per namespace would put every layer's
// write behind another handle's busy timeout for no gain. It is the same
// reason the home store exposes its one handle.
type Store struct {
	db     *sql.DB
	mu     sync.Mutex
	closed bool
	layers []*Layer
}

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

// Front puts the cache in front of one namespace under the given policy.
// The returned layer is owned by the store: Close shuts every layer down
// before the file goes away.
func (s *Store) Front(upstream namespace.Namespace, opts Options) *Layer {
	c := &Layer{Namespace: upstream, db: s.db, opts: opts,
		revalInflight: map[string]bool{}, subs: map[int]chan *pb.Event{}, dark: map[string]bool{}}
	c.pf.ctx, c.pf.cancel = context.WithCancel(context.Background())
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed { // opened after the store closed: this layer never warms
		c.pf.cancel()
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
		c.pf.cancel()
		c.pf.wg.Wait()   // the walk is out before its DB goes away
		c.revalWG.Wait() // and so is every in-flight revalidation
	}
	return s.db.Close()
}

// logErr surfaces a cache-side failure to the server log. A broken cache must
// never fail a live request, but under serve-first the cache IS the read
// path, so a cache that cannot remember is user-visible degradation: every
// read of an unremembered grid pays the source's full latency. The layer's
// noteCache is the surfacing half; this is only the log line.
func logErr(op string, err error) {
	if err != nil {
		log.Printf("gridwell: sourcecache %s: %v (answers are not being remembered)", op, err)
	}
}

// noteCache records one cache-write outcome and surfaces the transitions on
// the layer's own event stream as this namespace's health: down on the first
// failure, up on the first success after, never once per failure. "Degraded,
// I'm sure it will be okay" in a server log is how a broken cache runs
// unnoticed for hours — the strip is where the user actually looks.
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
// subscribers. The uuid rides empty: the fan-in fills in the namespace the
// event came from (rpc.QualifyEventIDs).
func (c *Layer) emitHealth(healthy bool, detail string) {
	ev := &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{
		Healthy: healthy, Detail: detail,
	}}}
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

// ── Info ────────────────────────────────────────────────────────────────

func (c *Layer) Info(ctx context.Context, in *pb.InfoRequest) (*pb.InfoResponse, error) {
	resp, err := c.Namespace.Info(ctx, in)
	c.noteReach(err, "", nil) // an unnamed call: reachability, no grid to re-read
	if err == nil {
		if b, merr := proto.Marshal(resp); merr == nil {
			_, werr := c.db.ExecContext(ctx, `INSERT INTO info (k, proto) VALUES ('info', ?)
				ON CONFLICT(k) DO UPDATE SET proto=excluded.proto`, b)
			c.noteCache("store info", werr)
		}
		// The layer always has an event stream to offer — its own synthetic
		// GridChanged and cache health, merged with the upstream's (see
		// Subscribe) — so the door it declares is its own. Flipped after the
		// store: what the source said is what is remembered.
		resp.Watch = true
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
	cached.Watch = true // the layer's door, same as the live path
	return cached, nil
}

// Handshake forwards the routed plugin list and remembers the answer per
// namespace, so a remote pane's + menu is readable while the source is dark.
// The contract is every other read's: serve stale on transport failures only,
// and verdicts pass through.
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

// ── GetGrid ─────────────────────────────────────────────────────────────

// GetGrid serves first and refreshes behind. A remembered grid answers
// immediately — the whole point: the source's latency, a gitlab walk or a
// remote round trip, never sits on the read path. Within freshWindow, and
// with the connection not known dark, the answer serves as-is; otherwise it
// serves stamped stale — this serve is a memory — and one background
// revalidation is kicked, whose landing emits a GridChanged so the client
// refetches. Only a miss waits on the source.
func (c *Layer) GetGrid(ctx context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	if cached, fetchedAt, hit := c.loadGrid(ctx, in.GridId); hit {
		if time.Since(time.Unix(fetchedAt, 0)) < c.window() && !c.isDark(sourceOf(in.GridId)) {
			return cached, nil
		}
		// The stale bit: this serve is a memory, and the wire says so. Past
		// the window because nothing has confirmed it since; inside the
		// window when the connection is known dark, because then a memory is
		// all it can be. Wire-only, never stored, so the revalidation
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
// miss path, the revalidation's read, and the prefetch walk, which exists to
// warm from the source and must never be answered by the rows it is warming.
//
// A stale answer is never remembered. The layer below degrades too — a
// plugin adapter whose source went dark answers from the rows it minted and
// stamps the grid — and storing that would permanently overwrite the good
// answer it degraded from with a poorer one, because the degraded read
// succeeds and nothing would correct it but a live read that may never come.
// The stale bit is the one place that fact is known, and this is the one
// place it is obeyed.
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

// revalidateGrid refreshes one remembered grid in the background, single-
// flight per grid id, on the layer's own context so a canceled click never
// kills a refresh other readers are counting on. A changed answer is stored
// and announced; a transport failure or a degraded (stale-stamped) answer
// changes nothing, the remembered rows stand; a verdict evicts, so the next
// read passes through and the verdict surfaces instead of a remembered ghost.
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
			// outlive the source's verdict. (A canceled ctx — the store
			// closing — reads as transport and evicts nothing.)
			c.evictGrid(ctx, gridID)
			c.emitGridChanged(gridID)
		}
	}()
}

// gridRespEqual reports whether two grid answers say the same thing, tiles
// compared by id so row order never fakes a change.
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

// evictGrid forgets a grid and its tile rows. Content and preview rows keyed
// by the tiles linger unreachable and refresh on their next live read: the
// file is disposable, and reaping them here would buy nothing.
func (c *Layer) evictGrid(ctx context.Context, gridID string) {
	_, err := c.db.ExecContext(ctx, `DELETE FROM tiles WHERE grid_id = ?`, gridID)
	c.noteCache("evict tiles", err)
	_, err = c.db.ExecContext(ctx, `DELETE FROM grids WHERE id = ?`, gridID)
	c.noteCache("evict grid", err)
}

// storeGrid replaces the grid row and its whole tile set in one transaction. A
// successful live GetGrid is by definition the complete list, so this is also
// how deletions that happened while dark reconcile.
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
		// answers under two keys — the old minted id and the new derived
		// address — and its tiles keep their ids, so a row may already exist
		// under the old grid key. The upsert moves it here; the remembered
		// answer under the old key decays as its tiles migrate, which is
		// right, since nothing asks for the old key again.
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

// ── GetTile ─────────────────────────────────────────────────────────────

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

// ── GetTilePreview ──────────────────────────────────────────────────────

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

// ── ReadContent ─────────────────────────────────────────────────────────

// ReadContent tees the live stream: the bytes are remembered as they pass and
// stored only at a clean end, because the cache holds complete values only and
// a partial body served later would be silent corruption. A transport-shaped
// failure before any chunk falls back to the remembered body; after a chunk has
// flowed the error passes through, since splicing cache into a half-live stream
// would fabricate a body nobody ever had.
func (c *Layer) ReadContent(ctx context.Context, in *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	var mediaType string
	var version int64
	var data []byte
	var gotChunk, oversized bool
	err := c.Namespace.ReadContent(ctx, in, func(ch *pb.ContentChunk) error {
		gotChunk = true
		// Chunk 1 carries media_type and version. A plugin sends it even for
		// empty content, so both always arrive before the end.
		if mediaType == "" && ch.GetMediaType() != "" {
			mediaType = ch.GetMediaType()
		}
		if version == 0 && ch.GetVersion() != 0 {
			version = ch.GetVersion()
		}
		if !oversized {
			data = append(data, ch.GetData()...)
			if len(data) > maxCachedContentBytes {
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

// sendChunked replays a remembered body in the live chunk shape — chunk 1
// carries the metadata, later chunks data only — so a caller cannot tell a
// remembered answer from a live one by its framing. One empty first chunk still
// goes out for an empty body, so the metadata always arrives.
func sendChunked(data []byte, emit func(b []byte, first bool) error) error {
	first := true
	for {
		n := min(len(data), contentChunkBytes)
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

// blob binds a body for one of the NOT NULL blob columns. A nil []byte binds
// as SQL NULL, and an empty answer — a tile nobody has typed into yet, a 200
// with no bytes — is an answer like any other: the cache must be able to
// remember it, and the columns say so with NOT NULL. Both body writers go
// through here, because the fault is the binding, not the column.
func blob(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// ── Subscribe ───────────────────────────────────────────────────────────

// Subscribe serves TWO streams as one: the upstream's, teed so the cache
// tracks the live session's mutations as the server relays them, and the
// layer's own — the GridChanged a background revalidation emits when it lands
// a different answer, and the health of the cache itself.
//
// Both, always, because they carry different facts and each is the only source
// of its own. The layer's stream is what closes the serve-first loop: stale
// served, refresh lands, event fires, the client refetches the corrected grid.
// Nothing upstream can know that happened. Serving ours only as a FALLBACK,
// when the upstream had none, silently dropped both facts for every namespace
// that does have a stream — which, now that the cache fronts connections
// alone, is every namespace it fronts. The tee, by contrast, is an
// accelerator: with nothing subscribed, reads still refresh on their own.
//
// Info declares watch on that basis: the layer always has a stream to offer.
func (c *Layer) Subscribe(ctx context.Context, in *pb.SubscribeRequest, send func(*pb.Event) error) error {
	// Every subscription and resubscription is a moment to warm the whole
	// source. In front of the transport that means the initial connect and a
	// re-dial of the server's own fan-in: one connection going dark and
	// coming back does not land here, because the stream this relays is the
	// transport's hub, which survives it. prefetch.go says what that costs.
	c.kickPrefetch()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// One send at a time: the two streams are relayed by two goroutines and
	// the caller's send is not concurrent.
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

// emitGridChanged announces one changed (or evicted) grid to the synthetic
// stream's subscribers. The id is this layer's local one; the server's fan-in
// qualifies it like any plugin event. A subscriber too far behind to take the
// event loses it — a missed refresh, not a missed fact, since the rows are
// already stored and the next read serves them — but never blocks a
// revalidation.
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
// so there is nothing to apply and the next successful GetGrid refreshes.
func (c *Layer) applyEvent(ctx context.Context, ev *pb.Event) {
	switch p := ev.GetPayload().(type) {
	case *pb.Event_TileChanged:
		c.upsertTile(ctx, p.TileChanged.GetTile())
	case *pb.Event_TileRemoved:
		c.deleteTile(ctx, p.TileRemoved.GetTileId())
	case *pb.Event_PluginHealth:
		// The source's own supervisor already knows whether it can be
		// reached, and says so on the stream this layer relays. Reading it
		// here is why a room re-entered after a machine died says it is a
		// memory without waiting for a call of this layer's own to fail. The
		// uuid is the source key as it arrives here — one segment for a
		// connection, deeper for something inside one, which is its own key
		// and never the connection's, since a far plugin being down does not
		// make the machine unreachable.
		// The client is receiving this same event, so nothing is emitted:
		// what it does with it is the client's half.
		c.setDark(p.PluginHealth.GetPluginUuid(), !p.PluginHealth.GetHealthy())
	}
}

// MintRef passes the mint through to the source. It is a write, not a read,
// so there is nothing to cache and nothing to serve when the source is dark:
// a reference that cannot be minted must not be stored. A source that does
// not derive ids answers itself, through namespace.MintRef.
func (l *Layer) MintRef(ctx context.Context, localID string) (string, error) {
	return namespace.MintRef(ctx, l.Namespace, localID)
}

// ── writes ──────────────────────────────────────────────────────────────

// Writes pass through untouched — the source stays the one owner of its
// truth — and a successful response updates the remembered rows, the same
// fold the event tee applies. Under serve-first this is what keeps a moved
// tile from snapping back to its remembered place on the next read and only
// correcting when the revalidation lands.

// foldWrite folds one in-place write's answer into the remembered rows. A
// write against a derived (key-form) tile mints its row, and the answer
// renames it: the remembered row under the requested id must go, or the
// remembered listing reads a ghost twin of the tile beside its minted self.
// Only for verbs that mutate the tile they name — a clone's request names
// the source, which stays.
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
// body rather than guessing at it: the bytes went by as caller frames, not a
// committed value, and a remembered body must be one the source vouched for
// whole. The tile row is updated (the version moved), and the next live read
// re-remembers the body — offline serves nothing rather than a body whose
// version lies.
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
