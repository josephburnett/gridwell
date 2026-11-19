package pane

// Crumb is one level of a pane's place, projected from the frame stack, so the
// chain cannot drift from what it describes. Anchor is a frame's own grid, its
// glyph; TileID the tile it was entered through, a link's door included. Level
// is the frame's index and the whole ascent arithmetic; ParentAnchor and
// ParentPath locate the place it was entered from.
type Crumb struct {
	Level  int
	Anchor string
	TileID string
	Text   bool

	ParentAnchor string
	ParentPath   []string
}

// Crumbs projects the stack, outermost first. The last crumb is where the
// pane is now; a boot-blank pane has none.
func (s *Stack) Crumbs() []Crumb {
	if s.Depth() == 1 && s.GridID == "" && s.Door == "" {
		return nil
	}
	frames := s.Frames()
	out := make([]Crumb, 0, len(frames))
	for i, f := range frames {
		c := Crumb{Level: i, Anchor: f.GridID, ParentAnchor: f.GridID}
		if f.GridID == "" || i > 0 && f.Door != "" {
			c.TileID, c.Text = f.Door, f.Content
			c.ParentAnchor, c.ParentPath = s.AnchorPathAt(i - 1)
		}
		out = append(out, c)
	}
	return out
}

// AscentsTo is how many ascents reach crumb c, and the one ascent arithmetic.
// Never negative, so clicking the crumb you are on does nothing.
func (s *Stack) AscentsTo(c Crumb) int {
	n := s.Depth() - 1 - c.Level
	if n < 0 {
		return 0
	}
	return n
}
