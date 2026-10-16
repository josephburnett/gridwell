package pane

import "slices"

// SelectionOwner names where the selection a pane draws and writes is held.
type SelectionOwner int

const (
	// SelectionNone: a content place shows no grid to select in.
	SelectionNone SelectionOwner = iota
	// SelectionRow: the grid's own row, one set every view of the grid shares
	// (2026-10-15).
	SelectionRow
	// SelectionLayout: inside a pane tile the layout blob holds each frame's
	// own set, so two panes on one grid keep two, and a select writes the
	// layout, never the grid row.
	SelectionLayout
)

// SelectionTarget is where the selection of the pane's place lives, the
// selection's twin of FramingTarget.
func (s *Stack) SelectionTarget(inPaneTile bool) SelectionOwner {
	switch {
	case s.Content:
		return SelectionNone
	case inPaneTile:
		return SelectionLayout
	}
	return SelectionRow
}

// Select sets the frame's selection, reporting whether it changed. The slice
// is replaced whole, never written in place, because a cloned stack shares it.
func (f *Frame) Select(ids []string) bool {
	next := slices.Clone(ids)
	slices.Sort(next)
	next = slices.Compact(next)
	if len(next) == 0 {
		next = nil
	}
	if slices.Equal(f.Selection, next) {
		return false
	}
	f.Selection = next
	return true
}

// Unselect takes tileID out of every frame's set in cur and in every tree a
// descent parked, reporting whether any set changed. The node's TileRemoved
// is its one caller's cue (events.Plan.Unselect): the tile left its grid, a
// set names only tiles on its grid, and a frame's set is the layout's, which
// the persister then writes like any change (2026-10-15).
func (s *Levels) Unselect(cur *Tree, tileID string) bool {
	changed := cur.unselect(tileID)
	for _, l := range s.frames {
		if l.OuterTree != nil && l.OuterTree.unselect(tileID) {
			changed = true
		}
	}
	return changed
}

func (t *Tree) unselect(tileID string) bool {
	changed := false
	t.Walk(func(p *Pane) {
		if p.Frame.unselect(tileID) {
			changed = true
		}
		for i := range p.below {
			if p.below[i].unselect(tileID) {
				changed = true
			}
		}
	})
	return changed
}

func (f *Frame) unselect(tileID string) bool {
	if !slices.Contains(f.Selection, tileID) {
		return false
	}
	return f.Select(slices.DeleteFunc(slices.Clone(f.Selection), func(id string) bool { return id == tileID }))
}
