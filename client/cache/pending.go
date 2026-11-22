package cache

import (
	"strconv"
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"google.golang.org/protobuf/proto"
)

// A create, a copy or a link is drawn from the gesture as the row it asks
// for, in its grid's Pending beside Tiles, until the node answers. Every view
// draws both (Grid.Drawn), and nothing else reads Pending, so no gesture,
// read or write can name an id the node has not minted: a press on a pending
// row hits nothing.

const provisional = "pending-"

// Provisional reports that id keys a pending row (Pend). It is bare, so no
// read asks for it either.
func Provisional(id string) bool { return strings.HasPrefix(id, provisional) }

// Pend draws n in its grid ahead of the create that asks for it, under a
// provisional id, until Landed files the node's row in its place or Failed
// or Released takes it away. Nil when n's grid is not cached.
func (c *Cache) Pend(n *gridwellv1.Tile) *Holding {
	if n == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.grids[n.GridId]
	if !ok {
		return nil
	}
	c.seq++
	row := proto.CloneOf(n)
	row.Id = provisional + strconv.FormatUint(c.seq, 10)
	if g.Pending == nil {
		g.Pending = map[string]*gridwellv1.Tile{}
	}
	g.Pending[row.Id] = row
	return &Holding{c: c, key: holdKey{row.Id, wroteRow}, seq: c.seq, origin: row}
}

// Drawn is every row the grid's views draw: its tiles, then the rows pending.
func (g *Grid) Drawn() []*gridwellv1.Tile {
	out := make([]*gridwellv1.Tile, 0, len(g.Tiles)+len(g.Pending))
	for _, n := range g.Tiles {
		out = append(out, n)
	}
	for _, n := range g.Pending {
		out = append(out, n)
	}
	return out
}

// unpend takes the pending row away and files resp, the node's row, in its
// grid: the create's answer is the node's word on a row no echo may have
// carried yet.
func (c *Cache) unpend(pending, resp *gridwellv1.Tile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, g := range c.grids {
		delete(g.Pending, pending.Id)
	}
	if resp != nil {
		c.storeLocked(c.admitLocked(resp, byRead), false)
	}
}
