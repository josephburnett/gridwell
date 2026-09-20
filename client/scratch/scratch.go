// Package scratch answers where a pane's ephemeral visits live, reading only
// Grid.scratch_grid_id, the stamp the serving node chains through mounts. A
// mounted remote grid's first segment is the local node, so the answer cannot
// be guessed from an id: it is three-valued, and an uncached grid is told not
// known rather than handed a guess.
package scratch

// Grid is what the rule reads. An uncached grid carries no stamp, which is why
// Cached is a field rather than an empty ScratchGridID.
type Grid struct {
	Cached        bool
	ScratchGridID string
}

// For returns the scratch grid that ephemeral visits from g land in. A cached
// grid whose node stamped none answers ("", true), a known nowhere.
func For(g Grid) (id string, known bool) {
	if !g.Cached {
		return "", false
	}
	return g.ScratchGridID, true
}

// Ephemeral reports whether a tile in tileGridID is an ephemeral visit from a
// pane standing on g. Unknown is not false: deleting one on ascent or promoting
// it needs a known yes, and writing something durable about it a known no.
func Ephemeral(g Grid, tileGridID string) (ephemeral, known bool) {
	id, known := For(g)
	if !known {
		return false, false
	}
	return id != "" && tileGridID == id, true
}
