package urlview

// Gen names one placed live view. The renderer mints it at place, a move keeps
// it, and main echoes it on the view's gone event, so a pane and a tile alone
// never decide which handle an event ends.
type Gen uint64

// Gens mints one Gen per place. Zero is never minted.
type Gens struct{ last Gen }

// Next is the Gen for the view about to be placed.
func (g *Gens) Next() Gen {
	g.last++
	return g.last
}

// GoneEnds reports whether main's gone event for view gone ends the handle
// held; a later view of the same tile in the same pane is not ended.
func GoneEnds(held, gone Gen) bool {
	return gone != 0 && held == gone
}
