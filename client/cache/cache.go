// Package cache holds the client-side per-grid tile cache and the
// reconciliation logic that applies Subscribe events to it.
//
// It is a separate package so the merge semantics are testable without a
// browser: the wasm layer calls Apply on each event from the subscribe
// stream.
package cache

import (
	"bytes"
	"maps"
	"sync"

	"github.com/josephburnett/gridwell/api/rpc"
)

// Cache stores grids and their tiles keyed by grid id. Concurrency-safe.
//
// The client always treats the server as canonical. Apply replaces existing
// rows by id; events arriving for unknown grids are dropped (the client
// fetches them on first descent).
type Cache struct {
	mu    sync.Mutex
	grids map[string]*Grid
	// content holds text tile bodies keyed by tile id — the single text-body
	// store. A body is fetched by tile id via ReadContent (routable; blob ids
	// are not) and written back for confirmed saves and for optimistic,
	// not-yet-saved edits. Keying by tile id makes every write tile-scoped:
	// editing one clone never touches a sibling's body.
	//
	// Each entry binds the bytes to the server version they derive from
	// (Base): one fact, "the content state this client has seen", with one
	// owner. Keeping the version anywhere else (on the grid row, say) lets a
	// foreign writer's event advance it while the stale bytes stay cached,
	// and the next save carries current-version with old-bytes past the
	// server's concurrency check. Saves claim SaveBasis (the entry's Base),
	// which only fetches and save responses advance, so a version is never
	// claimed apart from the bytes it vouches for.
	content map[string]*contentEntry
}

// contentEntry is a text tile's body plus its provenance. Base is the tile
// row version the bytes derive from; Dirty marks unsaved local edits (the
// optimistic buffer) that reconciliation must not throw away.
type contentEntry struct {
	data  []byte
	base  int64
	dirty bool
}

// Grid is a cached grid plus its tiles indexed by id for cheap upsert.
type Grid struct {
	Meta  rpc.Grid
	Tiles map[string]rpc.Tile
}

// New returns an empty cache.
func New() *Cache {
	return &Cache{grids: map[string]*Grid{}, content: map[string]*contentEntry{}}
}

// PutFetchedContent stores a body read from the server, paired with the tile
// row version the server read it under (ReadContent chunk-1 version). The
// entry is clean: server truth, no local edits riding on it.
//
// A dirty entry is never replaced: the fetch raced local unsaved edits (a
// keystroke typed during the fetch's flight, or an ascent flush that queued a
// save while the fetch was out). Overwriting would destroy the typing on
// screen and advance the basis a queued save claims at send time — the
// stale-bytes-with-current-version claim SaveBasis exists to prevent. The
// dirty entry's own save resolves it: accepted (basis current) or
// 409-reconciled (basis stale), either way through a path the user can see.
//
// A stale reply never regresses the basis. A fetch that was in flight while
// typing and an autosave completed lands last, carrying pre-edit bytes under
// an older version; the entry is clean by then, so only the version
// comparison stands between it and a visible rollback (the overlay repaints
// old bytes, the caret jumps, and the regressed basis manufactures a 409 on
// the next save). The basis moves forward or not at all.
func (c *Cache) PutFetchedContent(tileID string, data []byte, base int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.content[tileID]; ok && (e.dirty || base < e.base) {
		return
	}
	c.content[tileID] = &contentEntry{data: cloneBytes(data), base: base}
}

// PutEditedContent stores an optimistic, not-yet-saved local edit (textarea
// input) so the renderer reflects
// it immediately. The entry keeps its existing Base — the edit is based on
// the bytes already here — and turns dirty so reconciliation never discards
// it. An edit with no prior entry keeps Base 0: its save claims a version the
// server has moved past, fails the version check, and reconciles visibly —
// it can never silently overwrite anything.
func (c *Cache) PutEditedContent(tileID string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.content[tileID]
	if e == nil {
		e = &contentEntry{}
		c.content[tileID] = e
	}
	e.data = cloneBytes(data)
	e.dirty = true
}

// PutSavedContent stores the body a completed content write confirmed, with
// the response tile's version as the new base, so the next queued save chains
// from it. The entry is clean again: the server holds these bytes.
//
// Except when newer local edits landed while the save was in flight: the
// entry is dirty with different bytes than the ones this save confirmed. The
// cache entry is the one owner of unsaved typing — there is no DOM buffer to
// fall back on — so replacing it would destroy those keystrokes. Keep the
// newer bytes and their dirty mark; only the basis advances, and the
// follow-up save chains from the version this write established.
func (c *Cache) PutSavedContent(tileID string, data []byte, base int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.content[tileID]; ok && e.dirty && !bytes.Equal(e.data, data) {
		e.base = base
		return
	}
	c.content[tileID] = &contentEntry{data: cloneBytes(data), base: base}
}

// SaveBasis returns the version a content write for this tile must claim:
// the version of the bytes the user's edit is based on. Only content
// fetches and save responses advance it — a foreign writer's event advances
// the grid row version but never this, so a save based on bytes the client
// hasn't refreshed claims the old version and is rejected by the server
// instead of silently overwriting the foreign edit.
func (c *Cache) SaveBasis(tileID string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.content[tileID]
	if !ok {
		return 0, false
	}
	return e.base, true
}

// DirtyContent returns a copy of the tile's body iff the entry carries an
// unsaved local edit. The read every flush path uses: bytes come out keyed by
// the tile id they were edited under, so a flush cannot attribute one tile's
// buffer to another.
func (c *Cache) DirtyContent(tileID string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.content[tileID]
	if !ok || !e.dirty {
		return nil, false
	}
	return cloneBytes(e.data), true
}

// DirtyTileIDs returns the ids of every tile whose cached body carries an
// unsaved edit. The debounced save sweeps this list — pending edits are found
// by tile id, not by which pane or overlay happens to hold focus, so an edit
// can never be stranded by focus moving on before the timer fired.
func (c *Cache) DirtyTileIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for id, e := range c.content {
		if e.dirty {
			out = append(out, id)
		}
	}
	return out
}

func cloneBytes(b []byte) []byte {
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}

// DropTileContent forgets a tile's cached body so the next read refetches it
// from the server. The reconcile hook for a rejected optimistic edit: the
// server refused the write, so the cached bytes are client-only fiction and
// must not keep rendering as if saved.
func (c *Cache) DropTileContent(tileID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.content, tileID)
}

// TileContent returns the cached body for a plugin tile, or (nil, false) if
// absent. Bytes are returned by reference; treat as read-only.
func (c *Cache) TileContent(tileID string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.content[tileID]
	if !ok {
		return nil, false
	}
	return e.data, true
}

// PutGrid replaces a grid's metadata and tile set. Used after a fresh
// GetGrid call. Each replaced row runs the same content reconciliation as a
// Subscribe event (reconcileContent): a refetch and an event are the same
// fact arriving on two paths and must age cached bodies identically, or one
// path silently advances the version past the bytes.
func (c *Cache) PutGrid(g rpc.Grid, tiles []rpc.Tile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.grids[g.ID]
	gr := &Grid{Meta: g, Tiles: map[string]rpc.Tile{}}
	for _, n := range tiles {
		if old != nil {
			if cur, ok := old.Tiles[n.ID]; ok {
				c.reconcileContent(cur, n)
			}
		}
		gr.Tiles[n.ID] = n
	}
	c.grids[g.ID] = gr
}

// reconcileContent ages the cached body when a fresher row for the same tile
// arrives, whatever path it arrived on (Subscribe event or grid refetch).
// Callers hold c.mu.
//
// Text tiles: a row version beyond the entry's base means a foreign writer
// changed the content. Drop a clean entry so the next render refetches and
// the foreign edit becomes visible; keep a dirty one — the user's unsaved
// typing, whose save claims the old base, is rejected, and reconciles
// visibly through the conflict path. Same-version rows never drop: framing
// writes don't bump version and must not evict the body.
//
// Non-text tiles: version is not the content key (a pane tile's layout blob
// is framing-class and never bumps version), so a changed blob id is the
// staleness signal instead.
func (c *Cache) reconcileContent(cur, n rpc.Tile) {
	e, ok := c.content[n.ID]
	if !ok {
		return
	}
	if n.Kind == rpc.KindText {
		if !e.dirty && n.Version > e.base {
			delete(c.content, n.ID)
		}
		return
	}
	if n.BlobID != cur.BlobID {
		delete(c.content, n.ID)
	}
}

// Grid returns a snapshot of a cached grid, or (nil, false) if absent.
// The returned grid is a deep enough copy that the caller can iterate it
// without holding the cache lock.
func (c *Cache) Grid(id string) (*Grid, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.grids[id]
	if !ok {
		return nil, false
	}
	out := &Grid{Meta: g.Meta, Tiles: make(map[string]rpc.Tile, len(g.Tiles))}
	maps.Copy(out.Tiles, g.Tiles)
	return out, true
}

// KnownGridIDs returns the set of grid ids the cache currently holds.
func (c *Cache) KnownGridIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.grids))
	for id := range c.grids {
		out = append(out, id)
	}
	return out
}

// UpdateTile replaces a single tile row in the named grid. No-op if
// the grid or tile is not cached. Used by URLStream nav events to
// keep cached URL tiles in sync with in-page navigation without
// going through the full Subscribe event path.
func (c *Cache) UpdateTile(gridID string, t rpc.Tile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.grids[gridID]
	if !ok {
		return
	}
	if _, ok := g.Tiles[t.ID]; !ok {
		return
	}
	g.Tiles[t.ID] = t
}

// Apply consumes a Subscribe event and updates the cache. Returns true if
// any visible state changed (so the renderer knows whether to redraw).
//
// Unknown grids are not auto-fetched here; that's a UI policy decision the
// renderer makes when an event references a grid the user is currently
// looking at.
func (c *Cache) Apply(ev rpc.Event) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch ev.Kind {
	case rpc.EventTileChanged:
		if ev.TileChanged == nil {
			return false
		}
		n := ev.TileChanged.Tile
		g, ok := c.grids[n.GridID]
		if !ok {
			return false
		}
		// The optimistic-echo interlock: an event strictly older than the
		// cached row is a stale echo — a Subscribe event that lost the race
		// against the mutation response that already landed here (the
		// response row is version N; the echo of the previous state, N-1,
		// may still be in flight). Applying it would visibly roll the tile
		// back and then forward: mutation the user never made. Same-version
		// events still apply, because framing changes never bump version but
		// do change the framing columns.
		if cur, exists := g.Tiles[n.ID]; exists && n.Version < cur.Version {
			return false
		}
		if cur, exists := g.Tiles[n.ID]; exists {
			c.reconcileContent(cur, n)
		}
		g.Tiles[n.ID] = n
		return true
	case rpc.EventTileRemoved:
		if ev.TileRemoved == nil {
			return false
		}
		g, ok := c.grids[ev.TileRemoved.GridID]
		if !ok {
			return false
		}
		_, present := g.Tiles[ev.TileRemoved.TileID]
		// Drop the removed tile's clean cached body so a delete doesn't
		// strand content in the map forever, but spare a dirty one. A
		// cross-grid move emits TileRemoved(src) then TileChanged(dst) for
		// the same tile (store/place.go), so unsaved keystrokes must survive
		// the hop. Even for a genuine delete, discarding the user's unsaved
		// words silently is data loss: the flush sweep surfaces the orphan
		// instead.
		if e, ok := c.content[ev.TileRemoved.TileID]; !ok || !e.dirty {
			delete(c.content, ev.TileRemoved.TileID)
		}
		delete(g.Tiles, ev.TileRemoved.TileID)
		return present
	case rpc.EventGridChanged:
		// We can't update without a new GetGrid; signal redraw so the
		// caller can decide whether to refetch.
		return ev.GridChanged != nil
	}
	return false
}
