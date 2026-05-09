// Package cache holds the client-side per-grid node cache and the
// reconciliation logic that applies Subscribe events to it.
//
// Splitting this out of the WASM main lets us test the merge semantics
// without a browser: the WASM layer just calls Apply on each event from the
// SSE stream.
package cache

import (
	"sync"

	"github.com/josephburnett/ascent/internal/rpc"
)

// Cache stores grids and their nodes keyed by grid id. Concurrency-safe.
//
// The client always treats the server as canonical. Apply replaces existing
// rows by id; events arriving for unknown grids are dropped (the client
// fetches them on first descent).
type Cache struct {
	mu    sync.Mutex
	grids map[int64]*Grid
	blobs map[int64]Blob
}

// Blob is a cached file body (bytes + mime type) keyed by blob id.
type Blob struct {
	Data     []byte
	MimeType string
}

// Grid is a cached grid plus its nodes indexed by id for cheap upsert.
type Grid struct {
	Meta  rpc.Grid
	Nodes map[int64]rpc.Node
}

// New returns an empty cache.
func New() *Cache {
	return &Cache{grids: map[int64]*Grid{}, blobs: map[int64]Blob{}}
}

// PutBlob stores a blob. Used after a fresh GetBlob call.
func (c *Cache) PutBlob(blobID int64, data []byte, mime string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	c.blobs[blobID] = Blob{Data: cp, MimeType: mime}
}

// Blob returns the cached blob for blobID, or (Blob{}, false) if absent.
// Bytes are returned by reference; treat as read-only.
func (c *Cache) Blob(blobID int64) (Blob, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.blobs[blobID]
	return b, ok
}

// InvalidateBlob removes a blob from the cache so the next render forces a
// refetch. Called after the client itself writes new content for the file.
func (c *Cache) InvalidateBlob(blobID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.blobs, blobID)
}

// PutGrid replaces a grid's metadata and node set. Used after a fresh
// GetGrid call.
func (c *Cache) PutGrid(g rpc.Grid, nodes []rpc.Node) {
	c.mu.Lock()
	defer c.mu.Unlock()
	gr := &Grid{Meta: g, Nodes: map[int64]rpc.Node{}}
	for _, n := range nodes {
		gr.Nodes[n.ID] = n
	}
	c.grids[g.ID] = gr
}

// Grid returns a snapshot of a cached grid, or (nil, false) if absent.
// The returned grid is a deep enough copy that the caller can iterate it
// without holding the cache lock.
func (c *Cache) Grid(id int64) (*Grid, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.grids[id]
	if !ok {
		return nil, false
	}
	out := &Grid{Meta: g.Meta, Nodes: make(map[int64]rpc.Node, len(g.Nodes))}
	for k, v := range g.Nodes {
		out.Nodes[k] = v
	}
	return out, true
}

// KnownWellIDs returns the set of well row ids the cache currently holds.
// The pane layer uses this to truncate stale descent paths after deletes.
func (c *Cache) KnownWellIDs() map[int64]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[int64]bool{}
	for _, g := range c.grids {
		for id, n := range g.Nodes {
			if n.Type == "well" {
				out[id] = true
			}
		}
	}
	return out
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
	case rpc.EventNodeChanged:
		if ev.NodeChanged == nil {
			return false
		}
		n := ev.NodeChanged.Node
		g, ok := c.grids[n.GridID]
		if !ok {
			return false
		}
		g.Nodes[n.ID] = n
		return true
	case rpc.EventNodeRemoved:
		if ev.NodeRemoved == nil {
			return false
		}
		g, ok := c.grids[ev.NodeRemoved.GridID]
		if !ok {
			return false
		}
		_, present := g.Nodes[ev.NodeRemoved.NodeID]
		delete(g.Nodes, ev.NodeRemoved.NodeID)
		return present
	case rpc.EventGridChanged:
		// We can't update without a new GetGrid; signal redraw so the
		// caller can decide whether to refetch.
		return ev.GridChanged != nil
	case rpc.EventGridForked:
		// A well's child_grid_id was rewritten. Update the well node if
		// we have it.
		if ev.GridForked == nil {
			return false
		}
		for _, g := range c.grids {
			if n, ok := g.Nodes[ev.GridForked.WellID]; ok {
				n.ChildGridID = ev.GridForked.NewGridID
				g.Nodes[n.ID] = n
				return true
			}
		}
		return false
	}
	return false
}
