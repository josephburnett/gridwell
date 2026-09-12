package gesture

import "testing"

// String names the verdicts in failure messages; production code switches on
// them and never prints one.
func (r Recovery) String() string {
	switch r {
	case FinishLeftResize:
		return "FinishLeftResize"
	case FinishRightDrag:
		return "FinishRightDrag"
	case FinishLeftDrag:
		return "FinishLeftDrag"
	}
	return "FinishNothing"
}

func TestRecoverRelease(t *testing.T) {
	const (
		both = ButtonsLeft | ButtonsRight
		none = 0
	)
	tests := []struct {
		name    string
		buttons int
		armed   Armed
		want    Recovery
	}{
		{"nothing armed", none, Armed{}, FinishNothing},
		{"left resize, left still held", ButtonsLeft, Armed{LeftResize: true}, FinishNothing},
		{"left resize, left up", none, Armed{LeftResize: true}, FinishLeftResize},
		{"left resize, only right held", ButtonsRight, Armed{LeftResize: true}, FinishLeftResize},
		{"right drag, right still held", ButtonsRight, Armed{RightDrag: true}, FinishNothing},
		{"right drag, right up", none, Armed{RightDrag: true}, FinishRightDrag},
		{"right drag, only left held", ButtonsLeft, Armed{RightDrag: true}, FinishRightDrag},
		{"left drag, left still held", ButtonsLeft, Armed{Drag: true}, FinishNothing},
		{"left drag, left up", none, Armed{Drag: true}, FinishLeftDrag},
		{"left drag, only right held", ButtonsRight, Armed{Drag: true}, FinishLeftDrag},
		{
			name:    "resize outranks a right drag armed with it",
			buttons: none,
			armed:   Armed{LeftResize: true, RightDrag: true},
			want:    FinishLeftResize,
		},
		{
			name:    "resize outranks a left drag armed with it",
			buttons: none,
			armed:   Armed{LeftResize: true, Drag: true},
			want:    FinishLeftResize,
		},
		{
			name:    "a right drag outranks a left drag armed with it",
			buttons: none,
			armed:   Armed{RightDrag: true, Drag: true},
			want:    FinishRightDrag,
		},
		{
			name:    "a held right button leaves the left drag to finish",
			buttons: ButtonsRight,
			armed:   Armed{RightDrag: true, Drag: true},
			want:    FinishLeftDrag,
		},
		{
			// The copy and link drag is armed by the right button, so the
			// left button's release is not its release.
			name:    "a creating drag stays armed when the left button comes up",
			buttons: ButtonsRight,
			armed:   Armed{RightDrag: true, Drag: true, DragCreates: true},
			want:    FinishNothing,
		},
		{
			name:    "a creating drag finishes as the right drag it is",
			buttons: none,
			armed:   Armed{RightDrag: true, Drag: true, DragCreates: true},
			want:    FinishRightDrag,
		},
		{
			name:    "a creating drag with no right gesture armed is left alone",
			buttons: none,
			armed:   Armed{Drag: true, DragCreates: true},
			want:    FinishNothing,
		},
		{"every button held, everything armed", both, Armed{LeftResize: true, RightDrag: true, Drag: true}, FinishNothing},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			if got := RecoverRelease(c.buttons, c.armed); got != c.want {
				t.Errorf("RecoverRelease(%d, %+v) = %v, want %v", c.buttons, c.armed, got, c.want)
			}
		})
	}
}
