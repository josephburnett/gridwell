package gesture

// MouseEvent.buttons bits. buttons names what is held; MouseEvent.button,
// what changed.
const (
	ButtonsLeft  = 1
	ButtonsRight = 2
)

// Recovery names the commit path that finishes a release which arrived late.
type Recovery int

const (
	// FinishNothing: every armed gesture still holds its own button.
	FinishNothing Recovery = iota
	FinishLeftResize
	FinishRightDrag
	FinishLeftDrag
	FinishBand
	// FinishStripScroll lets go of a press dragging the + menu's strip.
	FinishStripScroll
)

// Armed is what a move finds armed. A left drag and a right-button gesture can
// be armed at once, so DragCreates says which button owns the drag.
type Armed struct {
	LeftResize bool
	RightDrag  bool
	Drag       bool
	// DragCreates is dragdrop.Intent.Creates: a copy or a link, armed by the
	// right button, so the left button's release is not its release.
	DragCreates bool
	// Pan is a drag that grabbed no tile and no swatch: it moves the viewport.
	Pan bool
	// Band is a shift + left-drag sweeping a band over the grid (SelectPress).
	Band bool
	// StripScroll is a left press on the + menu's strip between its items,
	// whose drag scrolls the strip: the finger's wheel.
	StripScroll bool
}

// InFlight reports whether any gesture is armed.
func (a Armed) InFlight() bool {
	return a.LeftResize || a.RightDrag || a.Drag || a.Band || a.StripScroll
}

// PressAct is what a press does, of any button and from either device.
type PressAct int

const (
	// PressStarts: pane focus follows the press and it starts its gesture.
	PressStarts PressAct = iota
	// PressFocuses: pane focus follows and nothing else (PressStartsGesture).
	PressFocuses
	// PressNothing: a gesture owns the pointer until its own button comes up,
	// so another button's press neither moves focus nor arms anything.
	PressNothing
)

// Press decides it from what is in flight.
func Press(transitioning bool, armed Armed) PressAct {
	switch {
	case armed.InFlight():
		return PressNothing
	case !PressStartsGesture(transitioning):
		return PressFocuses
	}
	return PressStarts
}

// Release is the commit path a mouseup of button finishes. Every gesture ends
// on its own button's release, so another button's, whose press did nothing
// (Press), finishes nothing; a creating drag ends only through the right drag
// it is.
func Release(button int, armed Armed) Recovery {
	switch {
	case armed.RightDrag && button == 2:
		return FinishRightDrag
	case armed.LeftResize && button == 0:
		return FinishLeftResize
	case armed.Drag && !armed.DragCreates && button == 0:
		return FinishLeftDrag
	case armed.Band && button == 0:
		return FinishBand
	case armed.StripScroll && button == 0:
		return FinishStripScroll
	}
	return FinishNothing
}

// RecoverRelease reads a move that reports a gesture's own button already up as
// that release arriving late, so the gesture finishes through its own commit
// path rather than being cleared.
func RecoverRelease(buttons int, armed Armed) Recovery {
	switch {
	case armed.LeftResize && buttons&ButtonsLeft == 0:
		return FinishLeftResize
	case armed.RightDrag && buttons&ButtonsRight == 0:
		return FinishRightDrag
	case armed.Drag && !armed.DragCreates && buttons&ButtonsLeft == 0:
		return FinishLeftDrag
	case armed.Band && buttons&ButtonsLeft == 0:
		return FinishBand
	case armed.StripScroll && buttons&ButtonsLeft == 0:
		return FinishStripScroll
	}
	return FinishNothing
}
