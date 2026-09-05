package palette

// What a BARE CLICK on a palette swatch means — press and release on the
// swatch with no drag between them.
//
// A drag has an obvious destination: the cell under the cursor. A click has
// none, so its meaning belongs to the swatch alone, and every swatch must
// have one, including "nothing". The popover floats over a pane, and the
// canvas underneath it is live: a click the palette does not claim reaches
// the canvas gesture behind it and descends into, or selects, whatever tile
// happens to sit at those coordinates. So the default is not "fall through",
// it is ClickNothing — and the caller reads this table instead of a chain of
// hand-written arms, where a kind added without one falls through again.

// ClickTarget is the one behavior a bare click on a swatch runs.
type ClickTarget int

const (
	// ClickNothing: this swatch does nothing on a click. The menu stays open
	// and the pane is untouched. The default, and the reason this is a table:
	// a swatch that only creates by being dragged must do NOTHING when
	// clicked, never something the canvas behind the popover would have done.
	ClickNothing ClickTarget = iota
	// ClickEnter: a plugin or connection row, or a plugin-declared root entry
	// — descend into the grid it roots.
	ClickEnter
	// ClickHere: the bar's promote crumb, which stands for the visit the pane
	// is already showing. Clicking where you already are does nothing, but it
	// is its own answer rather than the default, because the crumb is not a
	// creation swatch at all.
	ClickHere
	// ClickVisit: a primitive that declares a click behavior of its own — the
	// ephemeral visit a url or shell swatch opens without placing a tile.
	ClickVisit
)

// Swatch is what a bare click reads off one palette item: which of the three
// kinds of row it is, and — for a primitive — whether that kind declares a
// click behavior at all. Nothing here is a coordinate: a click has no
// destination, which is exactly why the swatch decides.
type Swatch struct {
	// IsPlugin: a plugin, connection, or declared-root row.
	IsPlugin bool
	// Promote: the bar's current-visit crumb, dragged as a template.
	Promote bool
	// Visits: this primitive's table row declares a click behavior.
	Visits bool
}

// ClickOn maps a swatch to its click behavior. The order is the precedence a
// row can carry more than one flag under: a plugin row's primitive field is
// its zero value, and the promote crumb is spelled as a url template, so the
// row's identity is asked for before its kind.
func ClickOn(s Swatch) ClickTarget {
	switch {
	case s.IsPlugin:
		return ClickEnter
	case s.Promote:
		return ClickHere
	case s.Visits:
		return ClickVisit
	default:
		return ClickNothing
	}
}
