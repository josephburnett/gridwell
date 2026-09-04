package pane

import "slices"

// A pane's place is one stack of frames.
//
// A frame is one doorway crossing: the grid you landed in, the tile you came
// through, and the viewport you have there. Descending through any doorway
// pushes a frame — a well, a link into another namespace, a text/url/shell
// tile (a frame with no grid: the tile is the place) — and every ascent pops
// one. The viewport you left a level at is the frame you left; there is no
// second saved-viewport stack and no separate in-namespace path. The URL
// (url.go) and the pane-tile layout blob (wire.go) are encodings of this
// stack; the bar's crumbs (chain.go) are a projection of it.

// Frame is one level of a pane's place.
//
// GridID is set only where it is authoritative: the bottom frame (the root
// grid the pane sits in) and a namespace crossing (a link's target grid). On
// an ordinary well frame it is empty and the grid is derived by walking Door
// from the frame below (gridpath.ResolveLeafGrid): the client cannot know a
// well's child grid until the parent grid is cached, and a derived fact must
// not be copied.
//
// Content marks a frame whose place is the door tile — a text, url, shell or
// page descent. Such a frame has no grid of its own; the viewport fields
// carry the text scroll and zoom instead.
type Frame struct {
	GridID  string
	Door    string
	Content bool

	Cx, Cy, Zoom float64

	// TextMode picks between "text" (raw markdown in the textarea overlay)
	// and "rendered" (the sanitized-HTML overlay) on a content frame.
	// TextScroll* is the scroll inside the content's interior in logical
	// pixels; TextZoom is the content's rendering scale.
	TextMode    string
	TextScrollX float64
	TextScrollY float64
	TextZoom    float64

	// MenuOpen records that the + menu was open on this level when the user
	// descended, so ascending back restores it — you come back with the menu
	// open, just as you left it.
	MenuOpen bool
}

// Footprint is a tile's cell rectangle in the grid it sits in — the only
// thing a content frame needs from the tile row.
type Footprint struct{ X, Y, W, H int64 }

// Center is the footprint's centre in the grid's own coordinates.
func (f Footprint) Center() (cx, cy float64) {
	return float64(f.X) + float64(f.W)/2, float64(f.Y) + float64(f.H)/2
}

// ContentFrame builds the frame a pane pushes when it lands inside a content
// tile: the tile is the place, so the viewport is the tile's own footprint
// centre at the zoom it is shown at, carrying the text mode and scroll it was
// left at.
//
// It is the one constructor, because every path that puts a pane inside a
// content tile must mint the same frame shape. A frame with no zoom reads as
// "never visited" (HasView), and the ascent out of it computes its overtake
// from nothing — so a path that minted a bare frame would leave a pane the
// descent path could never produce.
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

// HasView reports whether the frame carries a viewport the pane was actually
// left at. A frame restored from a URL or a layout blob has none — those
// encode the descent path, not the outer viewports, which are
// session-ephemeral — and the ascent onto it falls back to the grid's
// persisted framing instead of an arbitrary origin.
func (f Frame) HasView() bool { return f.Zoom > 0 }

// Stack is a pane's place: the frames it descended through, bottom first.
//
// The top frame is unrolled as the embedded Frame, so "where the pane is now"
// and "where it was" are the same shape, read through the same field names,
// with no copying between them. below holds the outer frames, outermost
// first. Push and Pop are the only writers of that boundary.
type Stack struct {
	Frame
	below []Frame
}

// NewStack returns a stack rooted at gridID with zoom 1.
func NewStack(gridID string) Stack {
	return Stack{Frame: Frame{GridID: gridID, Zoom: 1}}
}

// StackAt builds the stack a restored place names: a root grid, a descent
// path of doorway tile ids, and (when non-empty) a content descent on top.
// The outer frames carry no viewport — see Frame.HasView. This is the URL's
// decoder, and the layout blob's for a place its projection holds in full.
//
// It can only say one namespace level: a path is doorway ids below one
// anchor, so a stack whose crossings matter is decoded by StackOf instead.
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

// StackOf builds the stack from its frames, bottom first — the inverse of
// Frames, and the decoder for a place recorded level by level (wire.go's
// Place). Empty frames yield the boot-blank stack.
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

// ProjectionHolds reports whether Anchor/Path/ContentID name this whole
// stack: true when decoding that projection through StackAt rebuilds every
// frame's identity. It is false exactly where the projection loses a level —
// a namespace crossing below the top level, or a content frame below the top
// — and it decides by rebuilding and comparing rather than by a rule about
// which shapes lose, so no encoder can quietly drop a level it did not think
// of.
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

// Depth is the number of frames (1 = the pane sits at its root grid).
func (s *Stack) Depth() int { return len(s.below) + 1 }

// Frames returns every frame, bottom first, top included. The returned
// slice is fresh; the frames are copies.
func (s *Stack) Frames() []Frame {
	out := make([]Frame, 0, len(s.below)+1)
	out = append(out, s.below...)
	return append(out, s.Frame)
}

// Clone deep-copies the stack.
func (s *Stack) Clone() Stack {
	c := *s
	if len(s.below) > 0 {
		c.below = slices.Clone(s.below)
	}
	return c
}

// Push descends through a doorway: the current frame becomes an outer one
// and f is where the pane now is.
func (s *Stack) Push(f Frame) {
	s.below = append(s.below, s.Frame)
	s.Frame = f
}

// Pop ascends one level, restoring the frame below — including the viewport
// the pane was left at there. False (and no change) at the bottom.
func (s *Stack) Pop() bool {
	if len(s.below) == 0 {
		return false
	}
	s.Frame = s.below[len(s.below)-1]
	s.below = s.below[:len(s.below)-1]
	return true
}

// Popped returns the stack this one becomes after n ascents (clamped to the
// bottom) — the target place of a multi-level crumb jump, computed without
// touching the live pane so an animation can drive the landing.
func (s *Stack) Popped(n int) Stack {
	c := s.Clone()
	for i := 0; i < n && c.Pop(); i++ {
	}
	return c
}

// Reset replaces the whole stack with a single frame: the boot, URL-restore,
// and history-restore door. Nothing else clears the stack; a place is left by
// popping it.
func (s *Stack) Reset(f Frame) {
	s.below = nil
	s.Frame = f
}

// Anchor is the qualified grid id of the namespace level the pane is
// currently in: the GridID of the nearest frame at or below the top that
// carries one. Path's ids are relative to it.
func (s *Stack) Anchor() string {
	anchor, _ := s.AnchorPathAt(len(s.below))
	return anchor
}

// Path is the doorway tile ids from Anchor down to the pane's current grid.
// A content frame contributes nothing: it sits in the grid below it.
func (s *Stack) Path() []string {
	_, path := s.AnchorPathAt(len(s.below))
	return path
}

// AnchorPathAt is Anchor/Path as of level i — the place the crumb at that
// level names. A content frame's level resolves to the grid it sits in
// (the tile is a leaf of that grid, not a grid of its own).
func (s *Stack) AnchorPathAt(i int) (anchor string, path []string) {
	frames := s.Frames()
	if i < 0 {
		return "", nil
	}
	if i > len(frames)-1 {
		i = len(frames) - 1
	}
	// Walk back to the frame that opens the current namespace level.
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

// ContentID is the content tile the pane is descended into, or "".
func (s *Stack) ContentID() string {
	if !s.Content {
		return ""
	}
	return s.Door
}
