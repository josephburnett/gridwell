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
}

// Escape is Esc's verdict: ok when a gesture is armed, which then owns the
// key and is let go of whole, every arm dropped. With none armed Esc is not a
// gesture's.
func Escape(armed Armed) (u Undo, ok bool) {
	if !armed.LeftResize && !armed.RightDrag && !armed.Drag {
		return Undo{}, false
	}
	u.Dividers = armed.LeftResize
	u.View = armed.Drag && armed.Pan
	u.Ghost = armed.Drag && !armed.Pan
	return u, true
}
