package gesture

// Undo names what a drag already changed while it moved, which Esc puts back
// to where the press found it. Everything else a drag does waits for its
// release, so dropping its arm is the rest of the cancel.
type Undo struct {
	// Dividers: a pane resize moves the split ratios live, a crush included.
	Dividers bool
	// View: a pan moves the pane's viewport live.
	View bool
	// Ghost: a tile, clone, link, swatch or crumb drag returns its ghost to
	// where it was grabbed.
	Ghost bool
	// ClearSelection: nothing was armed, so Esc empties the selection of the
	// grid the focused pane shows.
	ClearSelection bool
}

// Idle is what Esc reads when no gesture is armed.
type Idle struct {
	// OnCanvas: the key reached the canvas, so no overlay, field or live
	// surface owns it.
	OnCanvas bool
	// GridPlace: the focused pane shows a grid, not a content tile.
	GridPlace bool
	// Selected: that grid's selection, as the pane draws it, is not empty.
	Selected bool
}

// Escape is Esc's verdict: ok when a gesture is armed, which then owns the
// key and is let go of whole, every arm dropped, a band sweep's included.
// With none armed, Esc clears the focused grid's selection when the key is
// the canvas's and there is one to clear; otherwise it is not Esc's to take.
func Escape(armed Armed, idle Idle) (u Undo, ok bool) {
	if !armed.LeftResize && !armed.RightDrag && !armed.Drag && !armed.Band {
		if idle.OnCanvas && idle.GridPlace && idle.Selected {
			return Undo{ClearSelection: true}, true
		}
		return Undo{}, false
	}
	u.Dividers = armed.LeftResize
	u.View = armed.Drag && armed.Pan
	u.Ghost = armed.Drag && !armed.Pan
	return u, true
}
