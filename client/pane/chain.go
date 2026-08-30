package pane

// Crumb is one level of a pane's place: the bottom bar renders one square per
// crumb, and clicking a crumb ascends the pane back to that level. The chain
// is a projection of the frame stack — one crumb per frame, in order — so it
// cannot drift from the place it describes.
//
// Exactly one of Anchor (a frame that opens a namespace level: its root
// grid) or TileID (a doorway or content tile) is set. Level is the frame's
// index in the stack, and is the whole of the ascent arithmetic: clicking
// the crumb at level L is Depth-1-L ascents. ParentAnchor/ParentPath locate
// a tile crumb's row (the grid it sits in) for preview and label rendering.
type Crumb struct {
	Level  int
	Anchor string
	TileID string
	Text   bool

	ParentAnchor string
	ParentPath   []string
}

// Crumbs projects the stack: one crumb per frame, outermost first. The last
// crumb is where the pane is now; a boot-blank pane (no grid yet) has none.
func (s *Stack) Crumbs() []Crumb {
	if s.Depth() == 1 && s.GridID == "" && s.Door == "" {
		return nil
	}
	frames := s.Frames()
	out := make([]Crumb, 0, len(frames))
	for i, f := range frames {
		if f.GridID != "" {
			out = append(out, Crumb{Level: i, Anchor: f.GridID, ParentAnchor: f.GridID})
			continue
		}
		anchor, path := s.AnchorPathAt(i - 1)
		out = append(out, Crumb{Level: i, TileID: f.Door, Text: f.Content,
			ParentAnchor: anchor, ParentPath: path})
	}
	return out
}

// AscentsTo is how many ascents reach crumb c from where the pane is now: 0
// when the pane is already there, so clicking the crumb you are on does
// nothing. Never negative. This is the one ascent arithmetic.
func (s *Stack) AscentsTo(c Crumb) int {
	n := s.Depth() - 1 - c.Level
	if n < 0 {
		return 0
	}
	return n
}
