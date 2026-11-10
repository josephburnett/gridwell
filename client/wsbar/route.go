package wsbar

import "github.com/josephburnett/gridwell/client/pane"

// Where a press in the bar goes. The bar is one row with no pane in it, so
// everything but a point outside the band is consumed, and one table answers
// both buttons: a gesture cannot mean one thing where it is drawn and another
// where it is dispatched.

// Action names what a press does. Every value but ActionPass consumes it.
type Action int

const (
	// ActionNone consumes the press and does nothing: empty space between
	// crumbs, the band beside the bar for any button but the left, and every
	// gesture the middle button makes.
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
	// handle, and for the left button the handle is the whole name area.
	ActionZoom
	// ActionWorkspaceRename edits Segment's workspace tile crumb.
	ActionWorkspaceRename
	// ActionLeaveLevels pops out to Segment's level, for a workspace tile crumb or
	// the leading close-only root.
	ActionLeaveLevels
	// ActionVisitDrag arms the drag of Segment's ephemeral visit row, as of
	// the tile it is; a release with no drag is the crumb's click.
	ActionVisitDrag
	// ActionAscend ascends to Segment's crumb.
	ActionAscend
	// ActionFocusColumn focuses the pane BandFocus names, which takes the bar.
	ActionFocusColumn
	// ActionSelect toggles Segment's tile in its grid's selection; see
	// pane.NavCrumb.SelectTarget.
	ActionSelect
)

// Click is the world state a press reads. X is relative to the bar's left
// edge, as Layout's segments are.
type Click struct {
	// Button is the DOM button: 0 left, 1 middle, 2 right.
	Button int
	// Shift, Ctrl and Meta are the keys held; touch's Select arrives as a
	// shift + left press (touchgest.Select).
	Shift, Ctrl, Meta bool
	X                 float64
	Zone              Zone
	BarW              float64
	Segments          []Segment
	Chain             []pane.NavCrumb
	// Title is the centered title's span, the right button's target;
	// ok=false when it did not fit.
	TitleX, TitleW float64
	TitleOK        bool
	// SlotMenu is whether the slot's mode has a right-click menu at all; see
	// circlemenu.For.
	SlotMenu bool
	// Visit is the chain index of the crumb standing for an ephemeral visit's
	// row, a drag handle for either button; VisitOK is false for none.
	Visit   int
	VisitOK bool
}

// Hit's Segment is the crumb the action names, for the three crumb actions.
type Hit struct {
	Action  Action
	Segment Segment
}

// RouteClick asks the zone, then the two fixed ends of the bar, then the
// chain, because the slot and the title sit over the band and not in it. A
// left press anywhere between the crumbs and the slot is a press on the name,
// so the zoom handle is as wide as the space the name is centered in; rename
// stays on the drawn text, so an edit is never offered off the name it edits.
// A select press (shift alone with the left button, as on a grid) toggles a
// tile crumb's tile and does nothing anywhere else in the band.
func RouteClick(in Click) Hit {
	switch {
	case in.Zone == ZoneOutside:
		return Hit{Action: ActionPass}
	case in.Button == 0 && in.Shift && !in.Ctrl && !in.Meta:
		if s, ok := At(in.Segments, in.X); ok && in.Zone == ZoneBar {
			if _, _, _, ok := in.Chain[s.Index].SelectTarget(); ok {
				return Hit{Action: ActionSelect, Segment: s}
			}
		}
		return Hit{Action: ActionNone}
	}
	switch in.Zone {
	case ZoneBand:
		if in.Button == 0 {
			return Hit{Action: ActionFocusColumn}
		}
		return Hit{Action: ActionNone}
	}
	inSlot := in.X >= in.BarW-SlotW
	inTitle := in.TitleOK && in.X >= in.TitleX && in.X < in.TitleX+in.TitleW
	inNameArea := in.TitleOK && in.X >= CrumbsEnd(in.Segments)
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
		s, ok := At(in.Segments, in.X)
		switch {
		case ok && in.Chain[s.Index].Workspace:
			return Hit{Action: ActionWorkspaceRename, Segment: s}
		case ok && in.VisitOK && s.Index == in.Visit:
			return Hit{Action: ActionVisitDrag, Segment: s}
		}
		return Hit{Action: ActionNone}
	}
	switch {
	case inSlot:
		return Hit{Action: ActionSlot}
	case inNameArea:
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
	case nc.Workspace || nc.CloseOnly:
		return Hit{Action: ActionLeaveLevels, Segment: s}
	case in.VisitOK && s.Index == in.Visit:
		return Hit{Action: ActionVisitDrag, Segment: s}
	}
	return Hit{Action: ActionAscend, Segment: s}
}
