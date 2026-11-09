package cache

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"google.golang.org/protobuf/proto"
)

// Placement is a tile's layout: its grid and footprint. A placement write
// claims the placement it saw, not a version, so the version interlock cannot
// order two of them.
type Placement struct {
	GridID     string
	X, Y, W, H int64
}

// PlacementOf is t's placement.
func PlacementOf(t *gridwellv1.Tile) Placement {
	return Placement{GridID: t.GetGridId(), X: t.GetX(), Y: t.GetY(), W: t.GetW(), H: t.GetH()}
}

func (p Placement) set(t *gridwellv1.Tile) {
	t.GridId, t.X, t.Y, t.W, t.H = p.GridID, p.X, p.Y, p.W, p.H
}

// hold is a fact the cache shows ahead of its echo, one of held. Neither
// claims a version, so until the stream carries it every row for the tile is
// older and takes the held fact; the stream is ordered, so nothing older
// follows the write's own echo. Once the write has landed, a read is the
// node's word too, which recovers an echo lost in a stream gap.
type hold struct {
	row    *gridwellv1.Tile // carries the held fact
	seq    uint64
	landed bool
}

type holdKey struct {
	id string
	w  Wrote
}

var held = []Wrote{WrotePlacement, WroteZoom}

// Holding is one write's claim on the cache, settled by its verdict. A nil
// Holding held nothing and settles nothing.
type Holding struct {
	c      *Cache
	key    holdKey
	seq    uint64
	origin *gridwellv1.Tile
}

// Move moves the cached row to p ahead of the write that asks for it and
// holds it there until that write's echo, its answer under a minted id, or
// its failure. Nil when the row is not cached.
func (c *Cache) Move(tileID string, p Placement) *Holding {
	return c.Hold(tileID, WrotePlacement, p.set)
}

// Hold sets the fact w names on the cached row by edit, ahead of the write
// that asks for it, and holds it as Move does. Nil when the row is not cached.
func (c *Cache) Hold(tileID string, w Wrote, edit func(*gridwellv1.Tile)) *Holding {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur := c.rowLocked(tileID)
	if cur == nil {
		return nil
	}
	c.seq++
	n := proto.CloneOf(cur)
	edit(n)
	key := holdKey{tileID, w}
	c.holds[key] = &hold{row: n, seq: c.seq}
	c.storeLocked(n, false)
	return &Holding{c: c, key: key, seq: c.seq, origin: cur}
}

// Landed folds the write's answer in for what it set, then lets a read
// decide the fact. An answer under another id is a source's minted row,
// whose echo will never name this one, so the hold ends there.
func (pl *Holding) Landed(resp *gridwellv1.Tile) {
	if pl == nil || resp == nil {
		return
	}
	pl.c.PutWriteResponse(resp.GridId, resp, pl.key.w)
	pl.c.mu.Lock()
	defer pl.c.mu.Unlock()
	h, ok := pl.c.holds[pl.key]
	switch {
	case !ok || h.seq != pl.seq:
	case resp.Id != pl.key.id:
		delete(pl.c.holds, pl.key)
	default:
		h.landed = true
	}
}

// Failed puts the fact back as it was, unless a later write of it has
// superseded this one or the stream has already spoken for it.
func (pl *Holding) Failed() { pl.end(true) }

// Released ends the hold of a write that did not land, leaving the row as it
// shows for the outbox's resend or a refusal's re-read.
func (pl *Holding) Released() { pl.end(false) }

func (pl *Holding) end(restore bool) {
	if pl == nil {
		return
	}
	c := pl.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if h, ok := c.holds[pl.key]; !ok || h.seq != pl.seq || (h.landed && !restore) {
		return
	}
	delete(c.holds, pl.key)
	if cur := c.rowLocked(pl.key.id); restore && cur != nil {
		n := proto.CloneOf(cur)
		pl.key.w.copy(n, pl.origin)
		c.storeLocked(n, false)
	}
}

// admitLocked is the row a held tile may store: n with each held fact n does
// not end the hold of. stream says n came on the Subscribe stream. Callers
// hold c.mu.
func (c *Cache) admitLocked(n *gridwellv1.Tile, stream bool) *gridwellv1.Tile {
	out := n
	for _, w := range held {
		key := holdKey{n.Id, w}
		h, ok := c.holds[key]
		if !ok {
			continue
		}
		at := w.same(n, h.row)
		if (stream && at) || (!stream && h.landed) {
			delete(c.holds, key)
			continue
		}
		if at {
			continue
		}
		if out == n {
			out = proto.CloneOf(n)
		}
		w.copy(out, h.row)
	}
	return out
}

// keepHeldLocked carries into a grid read the rows held there that the read
// predates; a read after the write landed ends the hold instead. Callers hold
// c.mu, before the read replaces the grid.
func (c *Cache) keepHeldLocked(gr *Grid) {
	id := gr.Meta.GetId()
	for key, h := range c.holds {
		if key.w != WrotePlacement || h.row.GridId != id || gr.Tiles[key.id] != nil {
			continue
		}
		if h.landed {
			delete(c.holds, key)
			continue
		}
		if old, ok := c.grids[id]; ok && old.Tiles[key.id] != nil {
			gr.Tiles[key.id] = old.Tiles[key.id]
		}
	}
}
