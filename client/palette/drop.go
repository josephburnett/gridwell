package palette

// What a template drag's release does. The + menu's swatches and the bar's
// promote crumb ride the same drag, so one verdict answers for every one of
// them, and a release that creates nothing still says why.

type Drop int

const (
	// DropSnapBack returns the ghost to its swatch and leaves the menu open.
	DropSnapBack Drop = iota
	// DropRefuse is a primitive over another node's grid: the swatch was gated
	// by the menu's node, so it refuses visibly rather than snapping back with
	// no reason given.
	DropRefuse
	// DropLink places an exit well onto the doorway's root grid.
	DropLink
	// DropPromote turns the bar's ephemeral visit into a persistent tile and
	// relocates the pane onto it.
	DropPromote
	// DropCreate creates the primitive the swatch names.
	DropCreate
)

// Release is the world state a drop reads. The caller resolves every field
// against the destination the drag-preview already settled on, so the create
// lands exactly where the preview said it may.
type Release struct {
	// Target is whether the release landed on a grid at all.
	Target bool
	// Occupied is whether the snapped cells already hold a tile.
	Occupied bool
	// Doorway marks a plugin, connection or declared-root swatch.
	Doorway bool
	// Enterable is pluginhealth.Classify == Enterable. DropOn reads it on the
	// doorway arm alone, so the caller may leave it false elsewhere.
	Enterable bool
	// Writable is the destination grid's writable bit. Unknown is not
	// writable: minting into a grid that may refuse the link would show a tile
	// the next read takes back.
	Writable bool
	// SameNode is whether the destination belongs to the node whose menu
	// offered the swatch.
	SameNode bool
	// Promote marks the bar's current-visit crumb.
	Promote bool
}

// DropOn asks the destination before the swatch, because a drop with nowhere
// to land is a snap-back whatever was dragged.
func DropOn(r Release) Drop {
	switch {
	case !r.Target, r.Occupied:
		return DropSnapBack
	case r.Doorway:
		if r.Enterable && r.Writable {
			return DropLink
		}
		return DropSnapBack
	case !r.SameNode:
		return DropRefuse
	case r.Promote:
		return DropPromote
	}
	return DropCreate
}
