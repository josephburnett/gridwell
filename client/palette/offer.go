package palette

// Which primitives the + menu offers on a grid. Both facts are the serving
// node's: the PTY rides that node's web door, so the client's own caps would
// be a second, wrong owner of the shells policy.

// Offer is what the grid and its node declare.
type Offer struct {
	// AcceptsTiles is Grid.accepts_tiles; unknown is false, so no swatch is
	// shown on a guess a drop would then refuse.
	AcceptsTiles bool
	// ShellsDisabled is the context node's disable_shells.
	ShellsDisabled bool
}

// Primitives reports whether the primitive row is offered at all.
func (o Offer) Primitives() bool { return o.AcceptsTiles }

// Shell reports whether the shell swatch is among them.
func (o Offer) Shell() bool { return o.AcceptsTiles && !o.ShellsDisabled }
