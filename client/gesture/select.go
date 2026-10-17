package gesture

import (
	"slices"

	"github.com/josephburnett/gridwell/api/rpc"
)

// The selection gestures: what a left press, a band and Esc do to the set of
// a grid's tiles the user has in hand, each as a Select naming what it adds
// and what it removes, never the whole set, so a select made over a set
// another writer has moved since stands beside that writer's.

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

// Select is one select: the tiles it adds and the tiles it removes, the
// SetSelection the grid row is sent. Each list is sorted, nil when empty.
type Select struct{ Add, Remove []string }

// Empty says the select names nothing.
func (s Select) Empty() bool { return len(s.Add) == 0 && len(s.Remove) == 0 }

// Apply is set after s, by the rule the node applies (rpc.ApplySelect).
func (s Select) Apply(set []string) []string { return rpc.ApplySelect(set, s.Add, s.Remove) }

// OnGrid is s adding only tiles on the grid now: a set names only its grid's
// tiles, so one that left is not carried into a write.
func (s Select) OnGrid(present func(id string) bool) Select {
	s.Add = canonical(slices.DeleteFunc(slices.Clone(s.Add), func(id string) bool { return !present(id) }))
	return s
}

// Toggle is a shift-tap on id over the shown set: it removes id when the set
// holds it, else adds it.
func Toggle(shown []string, id string) Select {
	if slices.Contains(shown, id) {
		return Select{Remove: []string{id}}
	}
	return Select{Add: []string{id}}
}

// Clear is Esc over the shown set: it removes what the pane shows, so a tile
// another writer selected since stays.
func Clear(shown []string) Select { return Select{Remove: canonical(shown)} }

// canonical is ids sorted with each once, nil when empty.
func canonical(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := slices.Clone(ids)
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

// Sweep adds every tile the band holds that the shown set lacks. A sweep only
// adds.
func Sweep(shown []string, b Band, tiles []Placed) Select {
	var add []string
	for _, t := range tiles {
		if b.Holds(t.X, t.Y, t.W, t.H) && !slices.Contains(shown, t.ID) {
			add = append(add, t.ID)
		}
	}
	return Select{Add: canonical(add)}
}
