// Package cache holds the client's per-grid tile cache and the reconciliation
// that applies Subscribe events to it.
package cache

import (
	"bytes"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"maps"
	"sort"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/josephburnett/gridwell/api/rpc"
)

// Cache stores grids and their tiles keyed by grid id, concurrency-safe. An
// event for an unknown grid is dropped; the client fetches it on descent.
type Cache struct {
	mu    sync.Mutex
	grids map[string]*Grid
	// dark is the sources whose latest health event said they are not
	// answering, keyed by that event's uuid.
	dark map[string]bool
	// content is the one body store, keyed by tile id because blob ids are
	// not routable and editing one clone must leave a sibling alone.
	content map[string]*contentEntry
}

// contentEntry is a body, the row version and blob it derives from, and
// whether it carries unsaved edits reconciliation must not discard. base is
// also the version a save claims.
type contentEntry struct {
	data  []byte
	base  int64
	blob  BlobBasis
	dirty bool
}

// BlobBasis is the blob id a body is filed under: for a fetch, the row's blob
// taken before the read; for a save, the blob its response row names.
type BlobBasis struct {
	id    int64
	known bool
}

// differs reports that a row naming blob may hold other bytes. An unknown
// basis vouches only for a row with no blob.
func (b BlobBasis) differs(blob int64) bool {
	if !b.known {
		return blob != 0
	}
	return blob != b.id
}

// behind reports that row n names bytes the entry does not hold. Version
// orders content edits; a pane layout mints a blob without a bump, so within
// one version the blob decides. Dirty bytes are never behind: their save
// reconciles them.
func (e *contentEntry) behind(n *gridwellv1.Tile) bool {
	switch {
	case e.dirty || n.Version < e.base:
		return false
	case n.Version > e.base:
		return true
	}
	return e.blob.differs(n.BlobId)
}

// Grid is a cached grid plus its tiles indexed by id.
type Grid struct {
	Meta  *gridwellv1.Grid
	Tiles map[string]*gridwellv1.Tile
}

// HostContent reports the grid's plugin-declared host_content, so its rows
// draw in the outside-Gridwell treatment.
func (g *Grid) HostContent() bool { return g != nil && g.Meta.HostContent }

// New returns an empty cache.
func New() *Cache {
	return &Cache{grids: map[string]*Grid{}, content: map[string]*contentEntry{}, dark: map[string]bool{}}
}

// AskContent is the basis a content read is filed under, taken before the
// read is sent and handed back to PutFetchedContent.
func (c *Cache) AskContent(tileID string) BlobBasis {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := c.rowLocked(tileID); n != nil {
		return BlobBasis{id: n.BlobId, known: true}
	}
	return BlobBasis{}
}

// PutFetchedContent stores a body read from the server under the version it
// was read at and the blob it was asked against. It never replaces a dirty
// entry or regresses the base (that manufactures a 409). A reply whose row
// moved blob mid-flight is not stored; only the blob is compared, because a
// version the read disagrees with would ask forever.
func (c *Cache) PutFetchedContent(tileID string, data []byte, base int64, asked BlobBasis) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.content[tileID]; ok && (e.dirty || base < e.base) {
		return
	}
	if n := c.rowLocked(tileID); n != nil && asked.differs(n.BlobId) {
		return
	}
	c.content[tileID] = &contentEntry{data: cloneBytes(data), base: base, blob: asked}
}

// PutEditedContent stores an optimistic, not-yet-saved edit, keeping the
// entry's base. With no prior entry the base is 0, so the save fails the
// version check and reconciles visibly rather than overwriting.
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

// PutSavedContent stores the body a content write confirmed, filed under the
// response row. When newer edits landed mid-flight only the basis advances:
// the cache entry is the one owner of unsaved typing.
func (c *Cache) PutSavedContent(row *gridwellv1.Tile, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	blob := BlobBasis{id: row.BlobId, known: true}
	if e, ok := c.content[row.Id]; ok && e.dirty && !bytes.Equal(e.data, data) {
		e.base, e.blob = row.Version, blob
		return
	}
	c.content[row.Id] = &contentEntry{data: cloneBytes(data), base: row.Version, blob: blob}
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
// edit.
func (c *Cache) DirtyContent(tileID string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.content[tileID]
	if !ok || !e.dirty {
		return nil, false
	}
	return cloneBytes(e.data), true
}

// DirtyTileIDs returns the ids of every tile with an unsaved edit.
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

// PutGrid replaces a grid's metadata and tile set after a fresh GetGrid,
// aging bodies exactly as an event would.
func (c *Cache) PutGrid(g *gridwellv1.Grid, tiles []*gridwellv1.Tile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	gr := &Grid{Meta: g, Tiles: map[string]*gridwellv1.Tile{}}
	for _, n := range tiles {
		c.ageContentLocked(n)
		c.evictElsewhereLocked(n.Id, g.Id)
		gr.Tiles[n.Id] = n
	}
	c.grids[g.Id] = gr
}

// evictElsewhereLocked gives a row one home: the grid that last answered for
// it. A move leaves the old grid's copy behind until that grid is read
// again, and a version cannot order the two, because a move claims none.
// Callers hold c.mu.
func (c *Cache) evictElsewhereLocked(tileID, home string) {
	for id, g := range c.grids {
		if id != home {
			delete(g.Tiles, tileID)
		}
	}
}

// ageContentLocked drops a clean body row n has moved past, whether or not
// its grid is cached. Callers hold c.mu.
func (c *Cache) ageContentLocked(n *gridwellv1.Tile) {
	if e, ok := c.content[n.Id]; ok && e.behind(n) {
		delete(c.content, n.Id)
	}
}

// rowLocked is the cached row for a tile id; evictElsewhereLocked keeps it
// in one grid. Callers hold c.mu.
func (c *Cache) rowLocked(tileID string) *gridwellv1.Tile {
	for _, g := range c.grids {
		if n, ok := g.Tiles[tileID]; ok {
			return n
		}
	}
	return nil
}

// Grid returns a snapshot: the map is a copy, the rows are the cached rows.
// A caller changing one clones it and hands it back through Apply or
// UpdateTile, so the version interlock still decides.
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
// namespace chain a health event names; rpc.ChainedThrough owns the rule.
func ServedBy(id, source string) bool {
	return source == EverySource || rpc.ChainedThrough(id, source)
}

// Reaches reports whether the namespace ns is source itself or lies behind
// it.
func Reaches(ns, source string) bool {
	return source == EverySource || ns == source || rpc.ChainedThrough(ns, source)
}

// NoteHealth folds one health event in and reports whether the source was dark
// before it. The empty uuid names no source and is dropped rather than read as
// EverySource, which would call every room in the client a memory.
func (c *Cache) NoteHealth(uuid string, healthy bool) (wasDark bool) {
	if uuid == EverySource {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	wasDark = c.dark[uuid]
	if healthy {
		delete(c.dark, uuid)
		return wasDark
	}
	c.dark[uuid] = true
	return wasDark
}

// SourceDark reports that the source serving gridID is not answering, so this
// room is a memory rather than an answer. Derived, never stored.
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

// ResyncSet is every cached grid served through source, sorted: one owner for
// both directions of a health transition.
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

// putTileLocked is the one door into a grid's tile map. A row strictly older
// than the cached one is refused; a same-version row applies, because framing
// never bumps version. Callers hold c.mu.
func (c *Cache) putTileLocked(g *Grid, n *gridwellv1.Tile) bool {
	cur, exists := g.Tiles[n.Id]
	if exists && n.Version < cur.Version {
		return false
	}
	c.ageContentLocked(n)
	c.evictElsewhereLocked(n.Id, g.Meta.GetId())
	g.Tiles[n.Id] = n
	return true
}

// UpdateTile folds one row read outside the Subscribe stream into the named
// grid. It never inserts, so a response routed through a leaf link cannot
// plant a foreign tile. A write's response goes through PutWriteResponse.
func (c *Cache) UpdateTile(gridID string, t *gridwellv1.Tile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if g, ok := c.cachedRowLocked(gridID, t); ok {
		c.putTileLocked(g, t)
	}
}

// cachedRowLocked is the grid holding t's row, or false after aging t's body,
// so a row this cache does not hold is never inserted. Callers hold c.mu.
func (c *Cache) cachedRowLocked(gridID string, t *gridwellv1.Tile) (*Grid, bool) {
	if g, ok := c.grids[gridID]; ok {
		if _, ok := g.Tiles[t.Id]; ok {
			return g, true
		}
	}
	c.ageContentLocked(t)
	return nil, false
}

// Wrote names what one write set, which is all its response may contribute:
// the rest of the response row is a snapshot an echo may already have
// overtaken at the same version, since framing claims none, and folding it
// in would put back a window the user has scrolled away from.
type Wrote int

const (
	// WroteBody is a content write's bytes: the blob, and the name a text
	// tile derives from its first line.
	WroteBody Wrote = iota + 1
	// WroteAddress is a url's address.
	WroteAddress
	// WroteName is a typed name.
	WroteName
	// WroteFrozen is the standing freeze, a framing write: no version.
	WroteFrozen
)

func (w Wrote) claimsVersion() bool { return w != WroteFrozen }

func (w Wrote) copy(dst, src *gridwellv1.Tile) {
	switch w {
	case WroteBody:
		dst.BlobId, dst.AltText = src.BlobId, src.AltText
	case WroteAddress:
		dst.UrlString = src.UrlString
	case WroteName:
		dst.AltText = src.AltText
	case WroteFrozen:
		dst.UrlFrozen = src.UrlFrozen
	}
}

// PutWriteResponse folds the row a write answered with into the named grid,
// taking only what w names and, for a content write, the version it claimed.
// A content response older than the cached row says nothing new. Like
// UpdateTile it never inserts.
func (c *Cache) PutWriteResponse(gridID string, resp *gridwellv1.Tile, w Wrote) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.cachedRowLocked(gridID, resp)
	if !ok {
		return
	}
	cur := g.Tiles[resp.Id]
	n := proto.CloneOf(cur)
	if w.claimsVersion() {
		if resp.Version < cur.Version {
			return
		}
		n.Version = resp.Version
	}
	w.copy(n, resp)
	c.putTileLocked(g, n)
}

// PatchTile folds an optimistic local change to one row in through the event
// path, and reports whether it landed. edit sees a clone.
func (c *Cache) PatchTile(t *gridwellv1.Tile, edit func(*gridwellv1.Tile)) bool {
	if t == nil {
		return false
	}
	patched := proto.CloneOf(t)
	edit(patched)
	return c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
		TileChanged: &gridwellv1.TileChanged{Tile: patched}}})
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
			c.ageContentLocked(n)
			c.evictElsewhereLocked(n.Id, n.GridId)
			return false
		}
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
		// A dirty body stays: a cross-grid move emits TileRemoved then
		// TileChanged, and the flush sweep surfaces an orphan.
		if e, ok := c.content[r.TileId]; !ok || !e.dirty {
			delete(c.content, r.TileId)
		}
		delete(g.Tiles, r.TileId)
		return present
	case *gridwellv1.Event_GridChanged:
		return p.GridChanged != nil
	}
	return false
}
