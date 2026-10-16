package cache

import (
	"slices"

	"google.golang.org/protobuf/proto"
)

// A grid's selection is the node's fact on its row (Grid.Meta.selection,
// filed by a read and replaced whole by SelectionChanged). A select is
// optimistic like a move: the set the user made is drawn from the press, and
// the node's word replaces it when it arrives or the write is refused.

// selHold is a grid's selection ahead of the node's word on it.
type selHold struct {
	// shown is the set a select put up and no write has carried yet, or one
	// still in flight; held says there is one.
	shown []string
	held  bool
	// landed is the set the node accepted last, until the stream speaks;
	// known says there is one.
	landed []string
	known  bool
}

// Selection is the set a grid's views draw, sorted: the one a select put up,
// else the one the node last accepted, else the one it told.
func (c *Cache) Selection(gridID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h := c.sel[gridID]; h != nil {
		if h.held {
			return sorted(h.shown)
		}
		if h.known {
			return sorted(h.landed)
		}
	}
	if g, ok := c.grids[gridID]; ok {
		return sorted(g.Meta.GetSelection())
	}
	return nil
}

// SelectionClaim is the set a write claims, sorted: the one the node holds as far as
// this client has heard, which is the last it accepted until the stream says
// otherwise.
func (c *Cache) SelectionClaim(gridID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h := c.sel[gridID]; h != nil && h.known {
		return sorted(h.landed)
	}
	if g, ok := c.grids[gridID]; ok {
		return sorted(g.Meta.GetSelection())
	}
	return nil
}

// Select puts set up as the grid's selection ahead of the write that carries
// it.
func (c *Cache) Select(gridID string, set []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.selHoldLocked(gridID)
	h.shown, h.held = slices.Clone(set), true
}

// SelectionLanded files the set a write carried as the node's. The set it put
// up is down once that is the set, so a later select still drawn stays.
func (c *Cache) SelectionLanded(gridID string, sent []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.selHoldLocked(gridID)
	h.landed, h.known = slices.Clone(sent), true
	if h.held && sameSet(h.shown, sent) {
		h.shown, h.held = nil, false
	}
}

// SelectionRefused drops everything ahead of the node's word, which the
// refusal's re-read of the grid brings.
func (c *Cache) SelectionRefused(gridID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sel, gridID)
}

func (c *Cache) selHoldLocked(gridID string) *selHold {
	h := c.sel[gridID]
	if h == nil {
		h = &selHold{}
		c.sel[gridID] = h
	}
	return h
}

// selectLocked replaces a cached grid's selection with the set the node told,
// whole. The stream orders the sets and every one is whole, so it is newer
// than any write's landing, and a set put up and not yet carried stays unless
// this is it. Callers hold c.mu.
func (c *Cache) selectLocked(gridID string, ids []string) bool {
	changed := false
	if h := c.sel[gridID]; h != nil {
		before := h.held || h.known
		h.landed, h.known = nil, false
		if h.held && sameSet(h.shown, ids) {
			h.shown, h.held = nil, false
		}
		if !h.held {
			delete(c.sel, gridID)
		}
		changed = before
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
