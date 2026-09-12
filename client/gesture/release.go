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
}

// RecoverRelease reads a move that reports a gesture's own button already up as
// that release arriving late, so the gesture finishes through its own commit
// path rather than being cleared. The order is onMouseUp's, and is the only
// place it is written down.
func RecoverRelease(buttons int, armed Armed) Recovery {
	switch {
	case armed.LeftResize && buttons&ButtonsLeft == 0:
		return FinishLeftResize
	case armed.RightDrag && buttons&ButtonsRight == 0:
		return FinishRightDrag
	case armed.Drag && !armed.DragCreates && buttons&ButtonsLeft == 0:
		return FinishLeftDrag
	}
	return FinishNothing
}
