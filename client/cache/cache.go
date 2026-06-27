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
	// content holds text tile bodies keyed by tile id — the single text-body
	// store. A body is fetched by tile id via GetTileContent (routable; blob ids
	// are not) and written back by PutTileContent, both for confirmed saves and
	// for optimistic, not-yet-saved edits. Keying by tile id makes every write
	// tile-scoped: editing one clone never touches a sibling's body. See
	// TileContent / PutTileContent.
	content map[string][]byte
}

// Grid is a cached grid plus its tiles indexed by id for cheap upsert.
type Grid struct {
	Meta  rpc.Grid
	Tiles map[string]rpc.Tile
}

// New returns an empty cache.
func New() *Cache {
	return &Cache{grids: map[string]*Grid{}, content: map[string][]byte{}}
}

// PutTileContent stores a text tile's body bytes keyed by tile id. This is the
// single text-body writer: confirmed saves (postUpdateText) and optimistic,
// not-yet-saved edits (rendered-mode keystrokes, raw textarea input, embed
// drops) all land here, so the renderer — which reads bodies through
// TileContent — always reflects an edit immediately. Keyed by tile id, so a
// write is tile-scoped and never leaks into a clone that shares content.
func (c *Cache) PutTileContent(tileID string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	c.content[tileID] = cp
}

// TileContent returns the cached body for a plugin tile, or (nil, false) if
// absent. Bytes are returned by reference; treat as read-only.
func (c *Cache) TileContent(tileID string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.content[tileID]
	return b, ok
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
		// Drop the removed tile's cached body so deleting a tile mid-edit doesn't
		// strand its content in the map forever.
		delete(c.content, ev.TileRemoved.TileID)
		delete(g.Tiles, ev.TileRemoved.TileID)
		return present
	case rpc.EventGridChanged:
		// We can't update without a new GetGrid; signal redraw so the
		// caller can decide whether to refetch.
		return ev.GridChanged != nil
	}
	return false
}
