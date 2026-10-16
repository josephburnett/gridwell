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
