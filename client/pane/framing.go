package pane

import "slices"

// The framing writeback and the liveness projection: the two decisions that
// read a pane's place and say who owns what. Both are projections of the
// frame stack (place.go), so neither can drift from where the pane actually
// is.

// FramingOwner names the row that owns the settled framing of a pane's
// current place — the ONE question every ascent and every settle tick asks
// (docs/simplify-plan.md S4: one framing writeback).
type FramingOwner struct {
	// Content: the place is a content tile, so what settles is its text
	// scroll, not grid framing. TileID is the content tile.
	Content bool
	// TileID is the DOORWAY tile the pane came into its grid through (the
	// content tile when Content). Empty at a root grid with no doorway.
	TileID string
	// DoorAnchor/DoorPath locate the doorway's own grid — the row lives
	// there, one level out.
	DoorAnchor string
	DoorPath   []string
	// RootGridID is the grid whose own row owns the framing when there is
	// no doorway. Always set for a grid place, so a caller whose doorway
	// lookup misses (a + menu portal has no tile in the origin grid) falls
	// back to it without a second rule.
	RootGridID string
}

// FramingTarget projects the pane's place onto the row that owns its
// framing: the doorway tile it came in by, or the grid itself at a root.
func (s *Stack) FramingTarget() FramingOwner {
	if s.Content {
		return FramingOwner{Content: true, TileID: s.Door}
	}
	anchor, path := s.AnchorPathAt(len(s.below))
	own := FramingOwner{RootGridID: anchor}
	if s.Door == "" {
		return own
	}
	own.TileID = s.Door
	if len(path) > 0 {
		own.DoorAnchor, own.DoorPath = anchor, slices.Clone(path[:len(path)-1])
	} else {
		// A namespace crossing: the link tile lives in the level below.
		own.DoorAnchor, own.DoorPath = s.AnchorPathAt(len(s.below) - 1)
	}
	return own
}

// PaneGrid names one pane and the grid it currently shows — the input to
// the framing-ownership rule between panes.
type PaneGrid struct {
	PaneID string
	GridID string
}

// FramingWriters applies the ONE-ACTIVE-SURFACE rule to grid framing
// (owner decision 2026-08-13, extending #249 from url/shell tiles to
// grids): when several panes show the SAME grid, exactly one — the
// focused pane — owns the framing writeback; the others are passive
// viewers. A pane that shares its grid with nobody always writes (it is
// trivially the active surface). Focusing a shared grid's sibling IS the
// takeover, exactly as opening a live tile elsewhere takes the stream.
// Before this rule every sibling wrote its own rect-derived values each
// settle tick, so the persisted framing thrashed nondeterministically.
func FramingWriters(panes []PaneGrid, focusedID string) map[string]bool {
	byGrid := map[string]int{}
	for _, p := range panes {
		byGrid[p.GridID]++
	}
	out := map[string]bool{}
	for _, p := range panes {
		out[p.PaneID] = byGrid[p.GridID] == 1 || p.PaneID == focusedID
	}
	return out
}

// Holder names a pane and the content tile it is descended into — the
// liveness projection's unit.
type Holder struct {
	PaneID string
	TileID string
}

// ContentPanes lists every pane of t descended into a content tile, in tree
// order. Liveness follows PANE EXISTENCE (owner decision #249): every pane
// that exists — including one parked in a stacked level — keeps its
// surface, so this is the whole answer to "which frames are parked live"
// for one tree.
func ContentPanes(t *Tree) []Holder {
	var out []Holder
	if t == nil {
		return nil
	}
	t.Walk(func(p *Pane) {
		if id := p.ContentID(); id != "" {
			out = append(out, Holder{PaneID: p.ID, TileID: id})
		}
	})
	return out
}

// TakeOver applies ONE LIVE SURFACE PER CONTENT TILE (owner decision #249):
// opening tileID live in openerID freezes every OTHER pane's surface on the
// same content — at any stack level — and returns those panes. The opener
// takes over; a pane that already holds this tile is not in the list (it
// keeps what it has, so a keep-alive return is idempotent).
func TakeOver(holders []Holder, openerID, tileID string) []string {
	var out []string
	for _, h := range holders {
		if h.TileID == tileID && h.PaneID != openerID {
			out = append(out, h.PaneID)
		}
	}
	return out
}
