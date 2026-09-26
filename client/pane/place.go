package pane

import "slices"

// A pane's place is one stack of frames: the grid you landed in, the tile you
// came through, and the viewport you have there. Every descent pushes one and
// every ascent pops one, so the viewport you left a level at is the frame you
// left, with no second saved-viewport stack and no separate in-namespace path.

// Frame is one level of a pane's place. GridID is set only where it is
// authoritative, on the bottom frame and on a namespace crossing; an ordinary
// well frame derives its grid through ResolveLeafGrid, since a derived fact
// must not be copied. Content marks a frame whose place is the door tile
// itself, with no grid of its own.
type Frame struct {
	GridID  string
	Door    string
	Content bool

	Cx, Cy, Zoom float64

	// TextMode picks the textarea overlay or the sanitized-HTML one.
	// TextScroll is inside the content's interior in logical pixels.
	TextMode    string
	TextScrollX float64
	TextScrollY float64
	TextZoom    float64

	// MenuOpen records that the + menu was open on this level, so ascending
	// back restores it.
	MenuOpen bool

	// ViewPending marks a frame restored from a layout blob: until Adopt, its
	// view is a placeholder that must never be written to its owner row.
	ViewPending bool
}

// Footprint is a tile's cell rectangle in the grid it sits in.
type Footprint struct{ X, Y, W, H int64 }

func (f Footprint) Center() (cx, cy float64) {
	return float64(f.X) + float64(f.W)/2, float64(f.Y) + float64(f.H)/2
}

// ContentFrame is the one constructor for a content descent's frame, because a
// frame with no zoom reads as never visited and the ascent out of it would
// compute its overtake from nothing.
func ContentFrame(tileID string, foot Footprint, zoom float64, textMode string, scrollX, scrollY float64) Frame {
	cx, cy := foot.Center()
	return Frame{
		Door: tileID, Content: true,
		Cx: cx, Cy: cy, Zoom: zoom,
		TextMode:    textMode,
		TextScrollX: scrollX,
		TextScrollY: scrollY,
	}
}

// HasView reports whether the frame carries a viewport the pane was left at. A
// frame restored from a URL or a layout blob has none, so the ascent onto it
// falls back to the grid's persisted framing rather than an arbitrary origin.
func (f Frame) HasView() bool { return f.Zoom > 0 && !f.ViewPending }

// Adopt settles a pending top frame on v's view and text state. A frame that
// is not pending keeps its own.
func (s *Stack) Adopt(v Frame) bool {
	if !s.ViewPending {
		return false
	}
	s.Cx, s.Cy, s.Zoom = v.Cx, v.Cy, v.Zoom
	s.TextMode, s.TextScrollX, s.TextScrollY = v.TextMode, v.TextScrollX, v.TextScrollY
	s.ViewPending = false
	return true
}

// Stack is a pane's place, bottom first. The top frame is unrolled as the
// embedded Frame, so where the pane is now and where it was are one shape read
// through one set of names. Push and Pop are the only writers of the boundary.
type Stack struct {
	Frame
	below []Frame
}

func NewStack(gridID string) Stack {
	return Stack{Frame: Frame{GridID: gridID, Zoom: 1}}
}

// StackAt builds the stack a restored place names: a root grid, a path of
// doorway ids, and a content descent on top, with no viewport on the outer
// frames. It says only one namespace level, so a stack whose crossings matter
// is decoded by StackOf.
func StackAt(gridID string, path []string, contentID string) Stack {
	s := Stack{Frame: Frame{GridID: gridID}}
	for _, id := range path {
		s.Push(Frame{Door: id})
	}
	if contentID != "" {
		s.Push(Frame{Door: contentID, Content: true})
	}
	return s
}

// StackOf builds the stack from its frames, bottom first: Frames' inverse, and
// the decoder for a place recorded level by level.
func StackOf(frames []Frame) Stack {
	if len(frames) == 0 {
		return Stack{}
	}
	s := Stack{Frame: frames[0]}
	for _, f := range frames[1:] {
		s.Push(f)
	}
	return s
}

// ProjectionHolds reports whether Anchor, Path and ContentID name this whole
// stack. It decides by rebuilding through StackAt and comparing, so no encoder
// can quietly drop a level it did not think of.
func (s *Stack) ProjectionHolds() bool {
	anchor, path := s.AnchorPathAt(len(s.below))
	rebuilt := StackAt(anchor, path, s.ContentID())
	have, want := s.Frames(), rebuilt.Frames()
	if len(have) != len(want) {
		return false
	}
	for i := range have {
		if have[i].GridID != want[i].GridID || have[i].Door != want[i].Door ||
			have[i].Content != want[i].Content {
			return false
		}
	}
	return true
}

// Depth is the number of frames; 1 is the pane at its root grid.
func (s *Stack) Depth() int { return len(s.below) + 1 }

// Frames returns every frame, bottom first. The slice is fresh and the frames
// are copies.
func (s *Stack) Frames() []Frame {
	out := make([]Frame, 0, len(s.below)+1)
	out = append(out, s.below...)
	return append(out, s.Frame)
}

func (s *Stack) Clone() Stack {
	c := *s
	if len(s.below) > 0 {
		c.below = slices.Clone(s.below)
	}
	return c
}

// Push descends through a doorway: the current frame becomes an outer one.
func (s *Stack) Push(f Frame) {
	s.below = append(s.below, s.Frame)
	s.Frame = f
}

// Pop ascends one level, restoring the frame below and the viewport the pane
// was left at there. False and no change at the bottom.
func (s *Stack) Pop() bool {
	if len(s.below) == 0 {
		return false
	}
	s.Frame = s.below[len(s.below)-1]
	s.below = s.below[:len(s.below)-1]
	return true
}

// Popped is the stack this one becomes after n ascents, clamped at the bottom
// and computed without touching the live pane, so an animation can drive the
// landing.
func (s *Stack) Popped(n int) Stack {
	c := s.Clone()
	for i := 0; i < n && c.Pop(); i++ {
	}
	return c
}

// Reset replaces the whole stack with a single frame: the boot and restore
// door. Nothing else clears the stack; a place is left by popping it.
func (s *Stack) Reset(f Frame) {
	s.below = nil
	s.Frame = f
}

// Anchor is the qualified grid id of the namespace level the pane is in.
// Path's ids are relative to it.
func (s *Stack) Anchor() string {
	anchor, _ := s.AnchorPathAt(len(s.below))
	return anchor
}

// Path is the doorway tile ids from Anchor down to the pane's grid. A content
// frame contributes nothing, sitting in the grid below it.
func (s *Stack) Path() []string {
	_, path := s.AnchorPathAt(len(s.below))
	return path
}

// AnchorPathAt is Anchor and Path as of level i. A content frame's level
// resolves to the grid it sits in.
func (s *Stack) AnchorPathAt(i int) (anchor string, path []string) {
	frames := s.Frames()
	if i < 0 {
		return "", nil
	}
	if i > len(frames)-1 {
		i = len(frames) - 1
	}
	start := 0
	for k := i; k >= 0; k-- {
		if frames[k].Content {
			continue
		}
		if frames[k].GridID != "" {
			start = k
			break
		}
	}
	anchor = frames[start].GridID
	for k := start + 1; k <= i; k++ {
		if frames[k].Content {
			continue
		}
		path = append(path, frames[k].Door)
	}
	return anchor, path
}

func (s *Stack) ContentID() string {
	if !s.Content {
		return ""
	}
	return s.Door
}
