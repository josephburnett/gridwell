// Package cache holds the client's per-grid tile cache and the reconciliation
// that applies Subscribe events to it, separate from the wasm layer so the
// merge semantics are testable without a browser.
package cache

import (
	"bytes"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"maps"
	"sort"
	"sync"

	"github.com/josephburnett/gridwell/api/rpc"
)

// Cache stores grids and their tiles keyed by grid id, concurrency-safe. An
// event for an unknown grid is dropped, since the client fetches that grid on
// first descent.
type Cache struct {
	mu    sync.Mutex
	grids map[string]*Grid
	// dark is the sources whose latest health event said they are not
	// answering, keyed by that event's uuid. It is what makes a room a
	// memory, and it lives beside the grids because the join from a source to
	// what it serves is ServedBy's, here. A launcher row's InfoError is the
	// handshake's separate record of a source that would not answer then.
	dark map[string]bool
	// content is the one text-body store, keyed by tile id because blob ids
	// are not routable and editing one clone must leave a sibling alone. Each
	// entry binds its bytes to the version they derive from, so a foreign
	// writer's event cannot advance a version over stale bytes.
	content map[string]*contentEntry
}

// contentEntry is a body, the row version it derives from, and whether it
// carries unsaved edits reconciliation must not discard.
type contentEntry struct {
	data  []byte
	base  int64
	dirty bool
}

// Grid is a cached grid plus its tiles indexed by id.
type Grid struct {
	Meta  *gridwellv1.Grid
	Tiles map[string]*gridwellv1.Tile
}

// HostContent reports the grid's declared host_content, so its rows draw in
// the outside-Gridwell treatment. The declaration is the plugin's, which is
// how the client never learns a plugin kind.
func (g *Grid) HostContent() bool { return g != nil && g.Meta.HostContent }

// New returns an empty cache.
func New() *Cache {
	return &Cache{grids: map[string]*Grid{}, content: map[string]*contentEntry{}, dark: map[string]bool{}}
}

// PutFetchedContent stores a body read from the server under the version it
// was read at. A dirty entry is never replaced: the fetch raced unsaved edits
// and its own save resolves them. A reply older than the entry's base is
// dropped too, since a regressed basis manufactures a 409 on the next save.
func (c *Cache) PutFetchedContent(tileID string, data []byte, base int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.content[tileID]; ok && (e.dirty || base < e.base) {
		return
	}
	c.content[tileID] = &contentEntry{data: cloneBytes(data), base: base}
}

// PutEditedContent stores an optimistic, not-yet-saved edit. The entry keeps
// its Base, since the edit is based on the bytes already here. With no prior
// entry Base stays 0, so the save fails the version check and reconciles
// visibly rather than overwriting.
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

// PutSavedContent stores the body a content write confirmed, with the
// response version as the new base so the next queued save chains from it.
// When newer edits landed mid-flight only the basis advances: the cache entry
// is the one owner of unsaved typing.
func (c *Cache) PutSavedContent(tileID string, data []byte, base int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.content[tileID]; ok && e.dirty && !bytes.Equal(e.data, data) {
		e.base = base
		return
	}
	c.content[tileID] = &contentEntry{data: cloneBytes(data), base: base}
}

// SaveBasis returns the version a content write must claim. Only fetches and
// save responses advance it, never a foreign writer's event, so a save on
// unrefreshed bytes is rejected rather than overwriting.
func (c *Cache) SaveBasis(tileID string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.content[tileID]
	if !ok {
		return 0, false
	}
	return e.base, true
}

// DirtyContent returns a copy of the tile's body when it carries an unsaved
// edit. Every flush path reads it.
func (c *Cache) DirtyContent(tileID string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.content[tileID]
	if !ok || !e.dirty {
		return nil, false
	}
	return cloneBytes(e.data), true
}

// DirtyTileIDs returns the ids of every tile with an unsaved edit. The
// debounced save sweeps this list rather than the focused overlay, so moving
// focus before the timer fires cannot strand an edit.
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

// DropTileContent forgets a tile's cached body so the next read refetches it.
// A rejected optimistic edit's bytes must not keep rendering as if saved.
func (c *Cache) DropTileContent(tileID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.content, tileID)
}

// TileContent returns the cached body by reference; treat it as read-only.
func (c *Cache) TileContent(tileID string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.content[tileID]
	if !ok {
		return nil, false
	}
	return e.data, true
}

// PutGrid replaces a grid's metadata and tile set after a fresh GetGrid. Each
// replaced row runs reconcileContent, because a refetch and an event are the
// same fact on two paths and must age cached bodies identically.
func (c *Cache) PutGrid(g *gridwellv1.Grid, tiles []*gridwellv1.Tile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.grids[g.Id]
	gr := &Grid{Meta: g, Tiles: map[string]*gridwellv1.Tile{}}
	for _, n := range tiles {
		if old != nil {
			if cur, ok := old.Tiles[n.Id]; ok {
				c.reconcileContent(cur, n)
			}
		}
		gr.Tiles[n.Id] = n
	}
	c.grids[g.Id] = gr
}

// reconcileContent ages the cached body when a fresher row for the same tile
// arrives on either path. Callers hold c.mu.
//
// For text, a version beyond the entry's base means a foreign writer: drop a
// clean entry so the next render refetches, keep a dirty one so its save is
// rejected and reconciles visibly. A same-version row never drops, since
// framing writes do not bump version. For other kinds version is not the
// content key, so a changed blob id is the staleness signal.
func (c *Cache) reconcileContent(cur, n *gridwellv1.Tile) {
	e, ok := c.content[n.Id]
	if !ok {
		return
	}
	if n.Kind == rpc.KindText {
		if !e.dirty && n.Version > e.base {
			delete(c.content, n.Id)
		}
		return
	}
	if n.BlobId != cur.BlobId {
		delete(c.content, n.Id)
	}
}

// Grid returns a snapshot: the map is a copy, the rows in it are the cached
// rows. A caller changing one clones it and hands the clone back through
// Apply or UpdateTile, so the version interlock still decides whether it
// lands.
func (c *Cache) Grid(id string) (*Grid, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.grids[id]
	if !ok {
		return nil, false
	}
	out := &Grid{Meta: g.Meta, Tiles: make(map[string]*gridwellv1.Tile, len(g.Tiles))}
	maps.Copy(out.Tiles, g.Tiles)
	return out, true
}

// EverySource names no source and so names them all. Subscribe has no cursor,
// so a stream gap says nothing about whose events it swallowed.
const EverySource = ""

// ServedBy reports whether a cached id is served through source, the
// namespace chain a health event names. A health uuid gains a segment per hop
// exactly as ids do, so a source's uuid is a chain prefix of every id it
// answers for; rpc.ChainedThrough owns that rule.
func ServedBy(id, source string) bool {
	return source == EverySource || rpc.ChainedThrough(id, source)
}

// Reaches reports whether the namespace ns is source itself or lies behind
// it. ServedBy answers for a thing a source serves; this answers for a source
// name, which is how a read about a node rather than its contents is keyed.
func Reaches(ns, source string) bool {
	return source == EverySource || ns == source || rpc.ChainedThrough(ns, source)
}

// NoteHealth folds one health transition in. The empty uuid names no source
// and is dropped rather than read as EverySource, which would call every room
// in the client a memory.
func (c *Cache) NoteHealth(uuid string, healthy bool) {
	if uuid == EverySource {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if healthy {
		delete(c.dark, uuid)
		return
	}
	c.dark[uuid] = true
}

// SourceDark reports that the source serving gridID is not answering, so this
// room is a memory rather than an answer. Derived, never stored and never on
// the wire: darkness is one fact and the bar's chip is its projection onto
// one room.
func (c *Cache) SourceDark(gridID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for src := range c.dark {
		if ServedBy(gridID, src) {
			return true
		}
	}
	return false
}

// ResyncSet is every cached grid served through source, sorted. One owner, so
// the down and up directions of a health transition cannot disagree, and no
// second copy of what serves what.
func (c *Cache) ResyncSet(source string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.grids))
	for id := range c.grids {
		if ServedBy(id, source) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// KnownGridIDs returns the set of grid ids the cache currently holds.
func (c *Cache) KnownGridIDs() []string { return c.ResyncSet(EverySource) }

// putTileLocked is the one door into a grid's tile map, so the interlock and
// reconcileContent belong to the map rather than to whichever caller
// remembered them. A row strictly older than the cached one is refused,
// whichever door it came from, since applying it would roll the tile back and
// then forward. A same-version row still applies, because framing changes
// never bump version but do change the framing columns. Callers hold c.mu.
func (c *Cache) putTileLocked(g *Grid, n *gridwellv1.Tile) bool {
	cur, exists := g.Tiles[n.Id]
	if exists && n.Version < cur.Version {
		return false
	}
	if exists {
		c.reconcileContent(cur, n)
	}
	g.Tiles[n.Id] = n
	return true
}

// UpdateTile folds one row the client learned outside the Subscribe stream
// into the named grid. It updates a row already held and never inserts, so a
// response routed through a leaf link cannot plant a foreign tile in a grid
// with no business holding it.
func (c *Cache) UpdateTile(gridID string, t *gridwellv1.Tile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.grids[gridID]
	if !ok {
		return
	}
	if _, ok := g.Tiles[t.Id]; !ok {
		return
	}
	c.putTileLocked(g, t)
}

// Apply consumes a Subscribe event, returning whether visible state changed.
// It never auto-fetches an unknown grid; that is the renderer's policy.
func (c *Cache) Apply(ev *gridwellv1.Event) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch p := ev.Payload.(type) {
	case *gridwellv1.Event_TileChanged:
		n := p.TileChanged.GetTile()
		if n == nil {
			return false
		}
		g, ok := c.grids[n.GridId]
		if !ok {
			return false
		}
		// Unlike UpdateTile, an event may insert a tile the cache has not
		// seen.
		return c.putTileLocked(g, n)
	case *gridwellv1.Event_TileRemoved:
		r := p.TileRemoved
		if r == nil {
			return false
		}
		g, ok := c.grids[r.GridId]
		if !ok {
			return false
		}
		_, present := g.Tiles[r.TileId]
		// A clean body goes, so a delete strands nothing, but a dirty one
		// stays: a cross-grid move emits TileRemoved then TileChanged for
		// the same tile, and discarding unsaved words silently is data
		// loss either way. The flush sweep surfaces the orphan.
		if e, ok := c.content[r.TileId]; !ok || !e.dirty {
			delete(c.content, r.TileId)
		}
		delete(g.Tiles, r.TileId)
		return present
	case *gridwellv1.Event_GridChanged:
		// Nothing to update without a new GetGrid; signal redraw so the
		// caller can decide whether to refetch.
		return p.GridChanged != nil
	}
	return false
}
