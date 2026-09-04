package pane

// A live surface — a native url WebContentsView or a shell overlay — belongs
// to one pane and to the descent that opened it. Whether it still belongs on
// screen this frame is one question, asked once per frame per surface, and
// both surface kinds ask it here. Two hand-written copies of the rule is how
// the url side came to have none: the shell sweep guarded on the pane's
// current descent and the url sweep positioned every handle it held, so a
// pane that moved on kept a native page pinned over whatever it showed next,
// swallowing every click over its rect.

// SurfaceVerdict is what one frame's sweep decides for one live surface.
type SurfaceVerdict int

const (
	// SurfacePark: the pane is not laid out in the current tree — it sits in
	// a stacked level, parked behind a pane tile. Every stacked level stays
	// alive, so the surface keeps running; it just has nowhere to be drawn.
	SurfacePark SurfaceVerdict = iota
	// SurfaceOrphan: the pane is on screen but is no longer inside the
	// descent this surface was opened for. Nothing on screen belongs to the
	// surface. What each kind does about it differs — a url view is torn
	// down, a shell overlay parks and keeps its session — but the verdict is
	// the same.
	SurfaceOrphan
	// SurfaceShow: the pane is on screen and still in that descent, so the
	// surface tracks the pane's content box.
	SurfaceShow
)

// SurfaceOf answers it for one surface.
//
//   - onScreen is whether the pane has a rect in this frame's layout, which
//     is also the answer to "is this pane in the current tree".
//   - paneContentID is the pane's current descent (Stack.ContentID), "" for a
//     pane looking at a grid and for a pane that has closed.
//   - descentID is the pane frame the surface was opened for. It is not
//     always the tile the surface shows: descending a url or shell LINK goes
//     live as the link's target, so the surface's tile id is the content
//     owner's while the frame carries the link row. Comparing the frame's own
//     id is what makes a live link neither orphaned nor re-placed every
//     frame.
func SurfaceOf(onScreen bool, paneContentID, descentID string) SurfaceVerdict {
	if !onScreen {
		return SurfacePark
	}
	if paneContentID == "" || paneContentID != descentID {
		return SurfaceOrphan
	}
	return SurfaceShow
}
