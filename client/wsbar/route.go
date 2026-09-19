package wsbar

import "github.com/josephburnett/gridwell/client/pane"

// Where a press in the bar goes. The bar is one row with no pane under it, so
// everything but a point outside the band is consumed, and one table answers
// both buttons: a gesture cannot mean one thing where it is drawn and another
// where it is dispatched.

// Action names what a press does. Every value but ActionPass consumes it.
type Action int

const (
	// ActionNone consumes the press and does nothing: the band beside the bar,
	// empty space between crumbs, and every gesture the middle button makes.
	ActionNone Action = iota
	// ActionPass leaves the press to the pane under it.
	ActionPass
	// ActionSlotMenu pops the circle slot's right-click menu; which menu that
	// is belongs to client/circlemenu, not to the bar.
	ActionSlotMenu
	// ActionSlot runs the circle slot's verdict; see barslot.
	ActionSlot
	// ActionRename edits the centered title.
	ActionRename
	// ActionZoom toggles the focused pane's zoom; the title is the pane's
	// handle.
	ActionZoom
	// ActionWorkspaceRename edits Segment's pane-tile crumb.
	ActionWorkspaceRename
	// ActionLeaveLevels pops out to Segment's level, for a pane-tile crumb or
	// the leading close-only root.
	ActionLeaveLevels
	// ActionPromote arms the drag that lands the current ephemeral visit on
	// the grid it is dropped on.
	ActionPromote
	// ActionAscend ascends to Segment's crumb.
	ActionAscend
)

// Click is the world state a press reads. X is relative to the bar's left
// edge, as Layout's segments are.
type Click struct {
	// Button is the DOM button: 0 left, 1 middle, 2 right.
	Button   int
	X        float64
	Zone     Zone
	BarW     float64
	Segments []Segment
	Chain    []pane.NavCrumb
	// Title is the centered title's span; ok=false when it did not fit.
	TitleX, TitleW float64
	TitleOK        bool
	// SlotMenu is whether the slot's mode has a right-click menu at all; see
	// circlemenu.For.
	SlotMenu bool
	// Promote is whether the pane's current visit is an ephemeral url, which
	// makes its crumb a drag handle rather than an ascent.
	Promote bool
}

// Hit's Segment is the crumb the action names, for the three crumb actions.
type Hit struct {
	Action  Action
	Segment Segment
}

// RouteClick asks the zone, then the two fixed ends of the bar, then the
// chain, because the slot and the title sit over the band and not in it.
func RouteClick(in Click) Hit {
	switch in.Zone {
	case ZoneOutside:
		return Hit{Action: ActionPass}
	case ZoneBand:
		return Hit{Action: ActionNone}
	}
	inSlot := in.X >= in.BarW-SlotW
	inTitle := in.TitleOK && in.X >= in.TitleX && in.X < in.TitleX+in.TitleW
	if in.Button == 2 {
		switch {
		case inSlot:
			if in.SlotMenu {
				return Hit{Action: ActionSlotMenu}
			}
			return Hit{Action: ActionNone}
		case inTitle:
			return Hit{Action: ActionRename}
		}
		if s, ok := At(in.Segments, in.X); ok && in.Chain[s.Index].PaneTile {
			return Hit{Action: ActionWorkspaceRename, Segment: s}
		}
		return Hit{Action: ActionNone}
	}
	switch {
	case inSlot:
		return Hit{Action: ActionSlot}
	case inTitle:
		if in.Button == 0 {
			return Hit{Action: ActionZoom}
		}
		return Hit{Action: ActionNone}
	}
	s, ok := At(in.Segments, in.X)
	if !ok || in.Button != 0 {
		return Hit{Action: ActionNone}
	}
	switch nc := in.Chain[s.Index]; {
	case nc.PaneTile || nc.CloseOnly:
		return Hit{Action: ActionLeaveLevels, Segment: s}
	case in.Promote && s.Index == len(in.Chain)-1:
		return Hit{Action: ActionPromote, Segment: s}
	}
	return Hit{Action: ActionAscend, Segment: s}
}
