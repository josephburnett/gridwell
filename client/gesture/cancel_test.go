package gesture

import "testing"

// Every gesture a held button drives is one of the three arms, and Esc lets
// go of each with only what its drag already changed put back. A preview that
// changed nothing needs no undo; its arm going is the whole cancel.
func TestEscape(t *testing.T) {
	tests := []struct {
		name  string
		armed Armed
		want  Undo
		ok    bool
	}{
		{"no drag: Esc passes through", Armed{}, Undo{}, false},
		{"pane resize, one divider or a corner's two", Armed{LeftResize: true}, Undo{Dividers: true}, true},
		{"crush: a resize pressed past the wall", Armed{LeftResize: true}, Undo{Dividers: true}, true},
		{"pan", Armed{Drag: true, Pan: true}, Undo{View: true}, true},
		{"tile move, in a pane or across one", Armed{Drag: true}, Undo{Ghost: true}, true},
		{"swatch drag-create and the crumb's promote drag", Armed{Drag: true}, Undo{Ghost: true}, true},
		{"right-drag clone", Armed{RightDrag: true, Drag: true, DragCreates: true}, Undo{Ghost: true}, true},
		{"ctrl + right-drag link", Armed{RightDrag: true, Drag: true, DragCreates: true}, Undo{Ghost: true}, true},
		{"tile resize, pane swap, pane split: previews only", Armed{RightDrag: true}, Undo{}, true},
		{
			name:  "a resize and a drag armed at once undo both",
			armed: Armed{LeftResize: true, Drag: true, Pan: true},
			want:  Undo{Dividers: true, View: true},
			ok:    true,
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Escape(c.armed)
			if got != c.want || ok != c.ok {
				t.Errorf("Escape(%+v) = %+v, %v; want %+v, %v", c.armed, got, ok, c.want, c.ok)
			}
		})
	}
}

// A cancel drops every arm, so what follows it is what follows no gesture at
// all: a move recovers nothing and a release finishes nothing, whichever
// button is still held or comes up.
func TestAfterEscapeNothingFinishes(t *testing.T) {
	for _, buttons := range []int{0, ButtonsLeft, ButtonsRight, ButtonsLeft | ButtonsRight} {
		if got := RecoverRelease(buttons, Armed{}); got != FinishNothing {
			t.Errorf("a move with buttons %d after Esc = %v, want FinishNothing", buttons, got)
		}
	}
	for _, button := range []int{0, 1, 2} {
		if got := Release(button, Armed{}); got != FinishNothing {
			t.Errorf("a release of button %d after Esc = %v, want FinishNothing", button, got)
		}
	}
}

func TestRelease(t *testing.T) {
	tests := []struct {
		name   string
		button int
		armed  Armed
		want   Recovery
	}{
		{"right up ends a right drag", 2, Armed{RightDrag: true}, FinishRightDrag},
		{"left up does not end a right drag", 0, Armed{RightDrag: true}, FinishNothing},
		{"left up ends a resize", 0, Armed{LeftResize: true}, FinishLeftResize},
		{"right up does not end a resize", 2, Armed{LeftResize: true}, FinishNothing},
		{"left up ends a left drag", 0, Armed{Drag: true}, FinishLeftDrag},
		{"a right drag's release outranks the left drag", 2, Armed{RightDrag: true, Drag: true}, FinishRightDrag},
		{"a creating drag is the right button's", 0, Armed{RightDrag: true, Drag: true, DragCreates: true}, FinishNothing},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			if got := Release(c.button, c.armed); got != c.want {
				t.Errorf("Release(%d, %+v) = %v, want %v", c.button, c.armed, got, c.want)
			}
		})
	}
}
