package cache

import (
	"slices"

	"google.golang.org/protobuf/proto"

	"github.com/josephburnett/gridwell/api/rpc"
)

// A grid's selection is the node's fact on its row (Grid.Meta.selection,
// filed by a read and replaced whole by SelectionChanged). A select is
// optimistic like a move, and names tiles, never the whole set: what is drawn
// is the set the node told with this client's selects over it, tile by tile,
// so another writer's select shows beside one still owed.

// selHold is a grid's selects ahead of the node's word on it.
type selHold struct {
	// up is each tile a select names that the node has not accepted: the
	// latest select's word on it.
	up map[string]tap
	// landed is each tile's word from the selects the node accepted, until
	// the stream tells a set.
	landed map[string]bool
	seq    uint64
}

// tap is a select's word on one tile; seq tells a later select of it apart.
type tap struct {
	in  bool
	seq uint64
}

// SelectSent is what one write carries: every tile up when it was sent.
type SelectSent struct {
	Add, Remove []string
	seqs        map[string]uint64
}

// Empty says the write would name nothing.
func (s SelectSent) Empty() bool { return len(s.Add) == 0 && len(s.Remove) == 0 }

// Selection is the set a grid's views draw, sorted: the set the node told,
// then what the node accepted since, then what is still owed.
func (c *Cache) Selection(gridID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var set []string
	if g, ok := c.grids[gridID]; ok {
		set = g.Meta.GetSelection()
	}
	h := c.sel[gridID]
	if h == nil {
		return sorted(set)
	}
	var add, remove []string
	for id, in := range h.landed {
		if in {
			add = append(add, id)
		} else {
			remove = append(remove, id)
		}
	}
	set = rpc.ApplySelect(set, add, remove)
	up := h.sent()
	return rpc.ApplySelect(set, up.Add, up.Remove)
}

// Select puts a select up ahead of the write that carries it.
func (c *Cache) Select(gridID string, add, remove []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.sel[gridID]
	if h == nil {
		h = &selHold{up: map[string]tap{}, landed: map[string]bool{}}
		c.sel[gridID] = h
	}
	each(add, remove, func(id string, in bool) {
		h.seq++
		h.up[id] = tap{in: in, seq: h.seq}
	})
}

// SelectionUp is every tile still owed, what a write sent now carries. A
// write parked and sent again reads it again, so no select waits on another's
// write.
func (c *Cache) SelectionUp(gridID string) SelectSent {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h := c.sel[gridID]; h != nil {
		return h.sent()
	}
	return SelectSent{}
}

func (h *selHold) sent() SelectSent {
	s := SelectSent{seqs: make(map[string]uint64, len(h.up))}
	for id, t := range h.up {
		if t.in {
			s.Add = append(s.Add, id)
		} else {
			s.Remove = append(s.Remove, id)
		}
		s.seqs[id] = t.seq
	}
	slices.Sort(s.Add)
	slices.Sort(s.Remove)
	return s
}

// SelectionLanded files a write the node accepted. A tile selected again
// since it was sent stays up.
func (c *Cache) SelectionLanded(gridID string, s SelectSent) { c.answered(gridID, s, true) }

// SelectionRefused takes down a write the node refused, so what is drawn
// falls back to the node's word.
func (c *Cache) SelectionRefused(gridID string, s SelectSent) { c.answered(gridID, s, false) }

func (c *Cache) answered(gridID string, s SelectSent, accepted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.sel[gridID]
	if h == nil {
		return
	}
	each(s.Add, s.Remove, func(id string, in bool) {
		if accepted {
			h.landed[id] = in
		}
		if t, ok := h.up[id]; ok && t.seq == s.seqs[id] {
			delete(h.up, id)
		}
	})
	if len(h.up) == 0 && len(h.landed) == 0 {
		delete(c.sel, gridID)
	}
}

func each(add, remove []string, fn func(id string, in bool)) {
	for _, id := range add {
		fn(id, true)
	}
	for _, id := range remove {
		fn(id, false)
	}
}

// selectLocked replaces a cached grid's selection with the set the node told,
// whole. The stream orders the sets and every one is whole, so it is newer
// than any write's landing; a select still in flight stays over it. Callers
// hold c.mu.
func (c *Cache) selectLocked(gridID string, ids []string) bool {
	changed := false
	if h := c.sel[gridID]; h != nil {
		changed = len(h.landed) > 0
		clear(h.landed)
		if len(h.up) == 0 {
			delete(c.sel, gridID)
		}
	}
	g, ok := c.grids[gridID]
	if !ok {
		return changed
	}
	if sameSet(g.Meta.GetSelection(), ids) {
		return changed
	}
	meta := proto.CloneOf(g.Meta)
	meta.Selection = slices.Clone(ids)
	g.Meta = meta
	return true
}

func sameSet(a, b []string) bool { return slices.Equal(sorted(a), sorted(b)) }

// sorted is nil for an empty set, so no set has two spellings.
func sorted(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return slices.Sorted(slices.Values(s))
}
