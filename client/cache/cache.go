// Package cache holds the client-side per-grid tile cache and the
// reconciliation logic that applies Subscribe events to it.
//
// Splitting this out of the WASM main lets us test the merge semantics
// without a browser: the WASM layer just calls Apply on each event from the
// SSE stream.
package cache

import (
	"maps"
	"sync"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// Cache stores grids and their tiles keyed by grid id. Concurrency-safe.
//
// The client always treats the server as canonical. Apply replaces existing
// rows by id; events arriving for unknown grids are dropped (the client
// fetches them on first descent).
type Cache struct {
	mu    sync.Mutex
	grids map[string]*Grid
	blobs map[int64][]byte
	// localBlobSeq generates client-local optimistic blob ids. It decrements
	// from 0, so optimistic ids are always negative and can never collide with
	// a server blob id (those are positive autoincrement rowids). See
	// OptimisticEdit.
	localBlobSeq int64
}

// Grid is a cached grid plus its tiles indexed by id for cheap upsert.
type Grid struct {
	Meta  rpc.Grid
	Tiles map[string]rpc.Tile
}

// New returns an empty cache.
func New() *Cache {
	return &Cache{grids: map[string]*Grid{}, blobs: map[int64][]byte{}}
}

// PutBlob stores a blob. Blobs are text bytes (the only blob-bearing tile
// kind is KindText). Used after a fresh GetBlob call.
func (c *Cache) PutBlob(blobID int64, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	c.blobs[blobID] = cp
}

// Blob returns the cached blob bytes for blobID, or (nil, false) if absent.
// Bytes are returned by reference; treat as read-only.
func (c *Cache) Blob(blobID int64) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.blobs[blobID]
	return b, ok
}

// OptimisticEdit reflects a not-yet-confirmed text edit to one tile, immediately
// and *tile-scoped*. It stores `data` under a fresh client-local blob id and
// repoints the tile to it, then returns true if the tile was found.
//
// Critically it does NOT mutate the blob the tile currently points at: blobs
// are content-addressed and shared (two clones of a text tile share one blob
// id), so overwriting it in place would leak the edit into every sibling that
// shares it — and that corrupted content would then be persisted the next time
// a sibling is saved. Repointing only this tile keeps clones independent.
//
// The authoritative server blob id arrives later via Apply(EventTileChanged),
// which replaces the tile (and drops this optimistic blob). A prior optimistic
// blob for the same tile is dropped here so the map can't grow without bound.
func (c *Cache) OptimisticEdit(gridID, tileID string, data []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.grids[gridID]
	if !ok {
		return false
	}
	t, ok := g.Tiles[tileID]
	if !ok {
		return false
	}
	if t.BlobID < 0 {
		delete(c.blobs, t.BlobID)
	}
	c.localBlobSeq--
	id := c.localBlobSeq
	cp := make([]byte, len(data))
	copy(cp, data)
	c.blobs[id] = cp
	t.BlobID = id
	g.Tiles[tileID] = t
	return true
}

// PutGrid replaces a grid's metadata and tile set. Used after a fresh
// GetGrid call.
func (c *Cache) PutGrid(g rpc.Grid, tiles []rpc.Tile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	gr := &Grid{Meta: g, Tiles: map[string]rpc.Tile{}}
	for _, n := range tiles {
		gr.Tiles[n.ID] = n
	}
	c.grids[g.ID] = gr
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
		// Reconcile any optimistic edit: if the tile was pointing at a
		// client-local optimistic blob, drop it now that the authoritative
		// server tile (with its real blob id) has arrived.
		if old, existed := g.Tiles[n.ID]; existed && old.BlobID < 0 && old.BlobID != n.BlobID {
			delete(c.blobs, old.BlobID)
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
		old, present := g.Tiles[ev.TileRemoved.TileID]
		// Release any pending optimistic blob the removed tile pointed at, so
		// deleting a tile mid-edit doesn't strand a client-local blob in the
		// map forever (mirrors the cleanup OptimisticEdit and the
		// EventTileChanged reconcile already do).
		if present && old.BlobID < 0 {
			delete(c.blobs, old.BlobID)
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
