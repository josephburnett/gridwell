package gesture

import "slices"

// The selection gestures: what a left press does to the set of a grid's tiles
// the user has in hand, and the set each one leaves. Every set comes back
// sorted, so the same set is the same bytes wherever it is written.

// SelectPress is fixed at a left press and never changes mid-gesture.
type SelectPress int

const (
	// SelectNone: the press is not a selection gesture and goes on as before.
	SelectNone SelectPress = iota
	// SelectToggle adds the pressed tile to its grid's selection, or takes it
	// out.
	SelectToggle
	// SelectBand sweeps a band of cells from the press; its release adds every
	// tile wholly inside it.
	SelectBand
)

// SelectInput is the press's facts.
type SelectInput struct {
	Shift, Ctrl, Meta bool
	// GridPlace: the pane shows a grid, not a content tile.
	GridPlace bool
	OverTile  bool
}

// ClassifySelect reads shift alone as the select key. Ctrl or cmd with it is
// another gesture's chord (SplitNav), and macOS reports ctrl + left as a
// right press, so no other key may alias it.
func ClassifySelect(in SelectInput) SelectPress {
	if !in.Shift || in.Ctrl || in.Meta || !in.GridPlace {
		return SelectNone
	}
	if in.OverTile {
		return SelectToggle
	}
	return SelectBand
}

// Toggle is set with id taken out if it was in, put in if it was not.
func Toggle(set []string, id string) []string {
	out := slices.DeleteFunc(slices.Clone(set), func(s string) bool { return s == id })
	if len(out) == len(set) {
		out = append(out, id)
	}
	return Canonical(out)
}

// Canonical is set sorted with each id once.
func Canonical(set []string) []string {
	out := slices.Clone(set)
	slices.Sort(out)
	return slices.Compact(out)
}

// Band is a rectangle of whole cells.
type Band struct{ X, Y, W, H int64 }

// BandOf spans the press's cell and the pointer's, both included, whichever
// way the drag went.
func BandOf(ax, ay, bx, by int64) Band {
	return Band{X: min(ax, bx), Y: min(ay, by), W: max(ax, bx) - min(ax, bx) + 1, H: max(ay, by) - min(ay, by) + 1}
}

// Holds reports whether a tile's footprint lies wholly inside the band. A tile
// the band only touches or overlaps is not swept (Joe, 2026-10-15).
func (b Band) Holds(x, y, w, h int64) bool {
	return x >= b.X && y >= b.Y && x+w <= b.X+b.W && y+h <= b.Y+b.H
}

// Placed is a tile's id and footprint on the swept grid.
type Placed struct {
	ID         string
	X, Y, W, H int64
}

// Sweep is set with every tile the band holds added. A sweep only adds.
func Sweep(set []string, b Band, tiles []Placed) []string {
	out := slices.Clone(set)
	for _, t := range tiles {
		if b.Holds(t.X, t.Y, t.W, t.H) {
			out = append(out, t.ID)
		}
	}
	return Canonical(out)
}

// OnGrid is set without the ids that are not tiles on the grid now: a set
// names only its grid's tiles, so one that left is not carried into a write.
func OnGrid(set []string, present func(id string) bool) []string {
	return slices.DeleteFunc(slices.Clone(set), func(id string) bool { return !present(id) })
}
