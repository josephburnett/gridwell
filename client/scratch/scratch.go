// Package scratch answers where a pane's ephemeral visits live, and whether a
// tile is one of them.
//
// An ephemeral visit — a url typed into the + menu, a shell opened from it —
// is a real row in a real grid, just not one on any pane: the SCRATCH grid,
// which the serving node stamps onto every grid it answers with
// (Grid.scratch_grid_id, chained through mounts). That stamp is the one owner
// of the fact, so this package reads it and nothing else. It cannot be
// guessed from an id: a mounted remote grid's first segment is the LOCAL
// node, so a roster lookup keyed on it answers with the local node's scratch
// grid for a grid that lives on another machine, and everything downstream —
// whether ascent deletes the row, whether a url state is persisted, whether a
// pane may be re-anchored — is then decided about the wrong node.
//
// So the answer is three-valued, and "not known yet" is one of the three. The
// caller reads the stamp when it is there, and when the grid is not cached it
// is told so rather than handed a guess.
package scratch

// Grid is the half of a grid this rule reads: whether the row is cached at
// all, and the scratch grid the serving node stamped on it. A grid that is
// not cached carries no stamp to read — that is the whole distinction, and it
// is why Cached is a field rather than an empty ScratchGridID.
type Grid struct {
	Cached        bool
	ScratchGridID string
}

// For returns the scratch grid that ephemeral visits from g land in, and
// whether that is known. An uncached grid answers ("", false): not known yet,
// never a guess. A cached grid whose node stamped none answers ("", true) —
// known, and the answer is "nowhere", which is what a visit from it must be
// told.
func For(g Grid) (id string, known bool) {
	if !g.Cached {
		return "", false
	}
	return g.ScratchGridID, true
}

// Ephemeral reports whether a tile living in tileGridID is an ephemeral visit
// from a pane standing on g, and whether that is known. Unknown is not false:
// a caller that acts on an ephemeral tile — deleting it on ascent, promoting
// it onto a grid — must have a known yes, and a caller about to write
// something durable about it must have a known no.
func Ephemeral(g Grid, tileGridID string) (ephemeral, known bool) {
	id, known := For(g)
	if !known {
		return false, false
	}
	return id != "" && tileGridID == id, true
}
