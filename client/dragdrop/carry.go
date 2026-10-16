package dragdrop

// Member is one tile a drag carries: its id and its footprint at the press.
type Member struct {
	ID         string
	X, Y, W, H int64
}

// Carry is the tiles one drag carries, the grabbed tile first.
type Carry []Member

// CarryOf is what a press on grabbed carries: its grid's whole selection when
// grabbed is in it, else grabbed alone, so a drag on an unselected tile
// leaves the selection be. tile reads a selected id's footprint; one the grid
// does not hold is not carried.
func CarryOf(grabbed Member, selection []string, tile func(id string) (Member, bool)) Carry {
	c := Carry{grabbed}
	in := false
	for _, id := range selection {
		if id == grabbed.ID {
			in = true
		}
	}
	if !in {
		return c
	}
	for _, id := range selection {
		if id == grabbed.ID {
			continue
		}
		if m, ok := tile(id); ok {
			c = append(c, m)
		}
	}
	return c
}

// Has reports whether the drag carries the tile id.
func (c Carry) Has(id string) bool {
	for _, m := range c {
		if m.ID == id {
			return true
		}
	}
	return false
}

// IDs lists the carried ids, the grabbed one first.
func (c Carry) IDs() []string {
	ids := make([]string, len(c))
	for i, m := range c {
		ids[i] = m.ID
	}
	return ids
}

// Offset is member i's place relative to the grabbed tile, in cells: the
// ghost's shape and the landing's.
func (c Carry) Offset(i int) (dx, dy int64) {
	return c[i].X - c[0].X, c[i].Y - c[0].Y
}

// At is every member's footprint when the grabbed tile lands at (x, y): one
// offset for all, so the set keeps its layout.
func (c Carry) At(x, y int64) Carry {
	out := make(Carry, len(c))
	for i, m := range c {
		dx, dy := c.Offset(i)
		out[i] = Member{ID: m.ID, X: x + dx, Y: y + dy, W: m.W, H: m.H}
	}
	return out
}

// Occupied refuses the drop for the whole set when any member, landed with
// the grabbed tile at (x, y), would overlap a tile the drop does not vacate.
// A move vacates the set's own cells and a copy or link vacates none; a grid
// is unbounded, so no cell is off it and a collision is the one refusal.
func (c Carry) Occupied(x, y int64, occupants []Member, vacates bool) bool {
	for _, l := range c.At(x, y) {
		for _, o := range occupants {
			if vacates && c.Has(o.ID) {
				continue
			}
			if RectsOverlap(l.X, l.Y, l.W, l.H, o.X, o.Y, o.W, o.H) {
				return true
			}
		}
	}
	return false
}

// MoveOrder is the order in which one placement per member, each its own
// write, lands the set at (x, y) within its grid without any landing on a
// member that has not moved yet: the node checks each placement against the
// grid as it stands. A translated set of disjoint rectangles always has such
// an order; members left over by an overlapping set go last, in carry order.
func (c Carry) MoveOrder(x, y int64) []int {
	to := c.At(x, y)
	moved := make([]bool, len(c))
	order := make([]int, 0, len(c))
	for len(order) < len(c) {
		next := -1
		for i := range c {
			if !moved[i] && !c.blocked(to[i], i, moved) {
				next = i
				break
			}
		}
		if next < 0 {
			break
		}
		moved[next] = true
		order = append(order, next)
	}
	for i := range c {
		if !moved[i] {
			order = append(order, i)
		}
	}
	return order
}

// blocked reports whether l, member i's landing, overlaps a member other than
// i still where the press found it.
func (c Carry) blocked(l Member, i int, moved []bool) bool {
	for j, m := range c {
		if j != i && !moved[j] && RectsOverlap(l.X, l.Y, l.W, l.H, m.X, m.Y, m.W, m.H) {
			return true
		}
	}
	return false
}
