package cache

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"google.golang.org/protobuf/proto"
)

// Placement is a tile's layout: its grid and footprint. It claims no version,
// so the version interlock cannot order two of them.
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

// hold is a placement the cache shows ahead of its echo. Until the stream
// carries it, every row for the tile is older and takes its layout; the
// stream is ordered, so nothing older follows the write's own echo. Once the
// write has landed, a read is the node's word too, which is what recovers an
// echo lost in a stream gap.
type hold struct {
	p      Placement
	seq    uint64
	landed bool
}

// Placing is one placement write's claim on the cache, settled by its
// verdict. A nil Placing held nothing and settles nothing.
type Placing struct {
	c      *Cache
	id     string
	seq    uint64
	origin Placement
}

// Place moves the cached row to p ahead of the write that asks for it and
// holds it there until that write's echo, its answer under a minted id, or
// its failure. Nil when the row is not cached.
func (c *Cache) Place(tileID string, p Placement) *Placing {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur := c.rowLocked(tileID)
	if cur == nil {
		return nil
	}
	c.seq++
	c.holds[tileID] = &hold{p: p, seq: c.seq}
	n := proto.CloneOf(cur)
	p.set(n)
	c.storeLocked(n, false)
	return &Placing{c: c, id: tileID, seq: c.seq, origin: PlacementOf(cur)}
}

// Landed folds the write's answer in for what it set, then lets a read
// decide the layout. An answer under another id is a plugin's minted row,
// whose echo will never name this one, so the hold ends there.
func (pl *Placing) Landed(resp *gridwellv1.Tile) {
	if pl == nil || resp == nil {
		return
	}
	pl.c.PutWriteResponse(resp.GridId, resp, WrotePlacement)
	pl.c.mu.Lock()
	defer pl.c.mu.Unlock()
	h, ok := pl.c.holds[pl.id]
	switch {
	case !ok || h.seq != pl.seq:
	case resp.Id != pl.id:
		delete(pl.c.holds, pl.id)
	default:
		h.landed = true
	}
}

// Failed puts the row back where it was, unless a later placement of the
// tile has superseded this one or the stream has already spoken for it.
func (pl *Placing) Failed() {
	if pl == nil {
		return
	}
	c := pl.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if h, ok := c.holds[pl.id]; !ok || h.seq != pl.seq {
		return
	}
	delete(c.holds, pl.id)
	if cur := c.rowLocked(pl.id); cur != nil {
		n := proto.CloneOf(cur)
		pl.origin.set(n)
		c.storeLocked(n, false)
	}
}

// admitLocked is the row a held tile may store: n with the held layout, or n
// itself when it ends the hold. stream says n came on the Subscribe stream.
// Callers hold c.mu.
func (c *Cache) admitLocked(n *gridwellv1.Tile, stream bool) *gridwellv1.Tile {
	h, ok := c.holds[n.Id]
	if !ok {
		return n
	}
	at := PlacementOf(n) == h.p
	if (stream && at) || (!stream && h.landed) {
		delete(c.holds, n.Id)
		return n
	}
	if at {
		return n
	}
	held := proto.CloneOf(n)
	h.p.set(held)
	return held
}

// keepHeldLocked carries into a grid read the rows held there that the read
// predates; a read after the write landed ends the hold instead. Callers hold
// c.mu, before the read replaces the grid.
func (c *Cache) keepHeldLocked(gr *Grid) {
	id := gr.Meta.GetId()
	for tileID, h := range c.holds {
		if h.p.GridID != id || gr.Tiles[tileID] != nil {
			continue
		}
		if h.landed {
			delete(c.holds, tileID)
			continue
		}
		if old, ok := c.grids[id]; ok && old.Tiles[tileID] != nil {
			gr.Tiles[tileID] = old.Tiles[tileID]
		}
	}
}
