package menu

// What a bare click on a menu swatch means. The popover floats over a live
// canvas, so a click the menu does not claim reaches the gesture behind it
// and acts on whatever tile sits there. Every swatch therefore names a
// behavior, and a table gives a new kind the ClickNothing default.

type ClickTarget int

const (
	// ClickNothing leaves the menu open and the pane untouched.
	ClickNothing ClickTarget = iota
	// ClickEnter descends into the grid a door swatch names.
	ClickEnter
	// ClickVisit opens the ephemeral visit without placing a tile.
	ClickVisit
	// ClickCopy visits an ephemeral copy of a template: the copy is made
	// first, and the template is never touched.
	ClickCopy
)

// Swatch carries no coordinate, because a click has no destination.
type Swatch struct {
	// IsSwatch marks a source, connection or declared-root row.
	IsSwatch bool
	// Visits marks a primitive whose table row declares a click behavior.
	Visits bool
	// Template marks a template; the copy swatch is a press with no click.
	Template, CopySwatch bool
}

// ClickOn asks identity before kind, because a row can carry more than one
// flag: a source row and a strip item carry the zero primitive.
func ClickOn(s Swatch) ClickTarget {
	switch {
	case s.Template:
		return ClickCopy
	case s.CopySwatch:
		return ClickNothing
	case s.IsSwatch:
		return ClickEnter
	case s.Visits:
		return ClickVisit
	default:
		return ClickNothing
	}
}
