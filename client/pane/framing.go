package pane

import "slices"

// The framing writeback and the liveness projection. Both are projections of
// the frame stack, so neither can drift from where the pane is.

// FramingOwner names the row that owns the settled framing of a pane's place,
// the one question every ascent and settle tick asks.
type FramingOwner struct {
	// Content: the place is a content tile, so what settles is its text
	// scroll, not grid framing.
	Content bool
	// TileID is the doorway the pane came in through, or the content tile.
	// Empty at a root grid with no doorway.
	TileID     string
	DoorAnchor string // the doorway's own grid, one level out
	DoorPath   []string
	// RootGridID owns the framing when there is no doorway. Always set for a
	// grid place, so a caller whose doorway lookup misses, as a + menu
	// portal's does, falls back without a second rule.
	RootGridID string
}

// FramingTarget is the row that owns the pane's framing: the doorway it came
// in by, or the grid itself at a root.
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
		own.DoorAnchor, own.DoorPath = s.AnchorPathAt(len(s.below) - 1)
	}
	return own
}

// PaneGrid names one pane and the grid it shows.
type PaneGrid struct {
	PaneID string
	GridID string
}

// FramingWriters applies the one-active-surface rule to grid framing: of
// several panes showing one grid only the focused one writes, because every
// sibling writing its own rect-derived values each settle tick thrashes the
// persisted framing.
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

// Holder names a pane and the content tile it is descended into.
type Holder struct {
	PaneID string
	TileID string
}

// Engagement is what going live on a content tile in one pane does to the one
// live surface that tile may have.
type Engagement struct {
	// Keep: the opener already holds the tile's surface, a keep-alive return.
	Keep bool
	// From is the pane whose surface moves to the opener whole: a url view with
	// its page, a terminal with its socket. Empty with Keep false: nobody
	// holds one, so a fresh surface is placed.
	From string
	// Close holds every surface on the tile beyond the one kept or moved. The
	// rule allows one, so this is empty unless the rule was already broken.
	Close []string
}

// TakeOver applies one live surface per content tile: opening tileID in
// openerID takes the surface another pane holds on the same content, at any
// stack level, rather than making a second one.
func TakeOver(holders []Holder, openerID, tileID string) Engagement {
	var e Engagement
	for _, h := range holders {
		switch {
		case h.TileID != tileID:
		case h.PaneID == openerID:
			e.Keep = true
		case e.From == "":
			e.From = h.PaneID
		default:
			e.Close = append(e.Close, h.PaneID)
		}
	}
	if e.Keep && e.From != "" {
		e.Close, e.From = append([]string{e.From}, e.Close...), ""
	}
	return e
}

// Heir names the pane a closing pane's surface on tileID moves to instead of
// closing: the first of returning, the panes coming back on screen each named
// with the content tile it is descended into, that shows the tile and holds
// no surface. "" means the tile leaves every pane, and the surface closes.
func Heir(tileID string, returning, holders []Holder) string {
	for _, r := range returning {
		held := slices.ContainsFunc(holders, func(h Holder) bool { return h.PaneID == r.PaneID })
		if r.TileID == tileID && !held {
			return r.PaneID
		}
	}
	return ""
}
