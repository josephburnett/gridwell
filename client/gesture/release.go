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
}

// Release is the commit path a mouseup of button finishes. A left drag ends on
// any button's release; a creating drag ends only through the right drag it
// is.
func Release(button int, armed Armed) Recovery {
	switch {
	case armed.RightDrag && button == 2:
		return FinishRightDrag
	case armed.LeftResize && button == 0:
		return FinishLeftResize
	case armed.Drag && !armed.DragCreates:
		return FinishLeftDrag
	case armed.Band && button == 0:
		return FinishBand
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
	}
	return FinishNothing
}
