package gesture

// Surface is what a content descent shows in the pane a press hit.
type Surface int

const (
	// SurfaceFace is a frozen face: a shell or url descent with no live
	// surface, or a descent whose row has not landed.
	SurfaceFace Surface = iota
	SurfaceRawText
	SurfaceRendered
	SurfaceShell
	SurfaceURL
)

// Landing is what a left press does once the canvas, rather than the
// surface's own element, received it inside a content descent. That happens
// when the surface was not there to take it: the textarea and the rendered
// view live only over the focused pane, and an open menu parks every live
// surface. Pane focus has already followed the press; the landing is the rest
// of the click, so no press is focus-only.
type Landing int

const (
	// LandPane: nothing in the pane takes a click, so the press only gives
	// the pane the keyboard.
	LandPane Landing = iota
	// LandCaret puts the textarea's caret where the painted text was hit.
	LandCaret
	// LandElement clicks the rendered element under the point: a task
	// checkbox or a link.
	LandElement
	// LandTerminal gives the terminal the keyboard.
	LandTerminal
	// LandAscend is the raw-text edge press, which ascends as it does on the
	// focused pane's textarea.
	LandAscend
)

// Land decides it. inText is the point inside the text inner box. A url view
// the canvas received a press for was parked, and its page never saw the
// press, so the press lands on the pane alone.
func Land(s Surface, inText bool) Landing {
	switch s {
	case SurfaceRawText:
		if inText {
			return LandCaret
		}
		return LandAscend
	case SurfaceRendered:
		if inText {
			return LandElement
		}
	case SurfaceShell:
		return LandTerminal
	}
	return LandPane
}
