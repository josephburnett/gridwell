package pane

// A live surface belongs to one pane and to the descent that opened it.
// Whether it still belongs on screen is one question, and both surface kinds
// ask it here.

// SurfaceVerdict is one frame's sweep for one live surface.
type SurfaceVerdict int

const (
	// SurfacePark: the pane sits in a stacked level, which stays alive, so the
	// surface keeps running with nowhere to be drawn.
	SurfacePark SurfaceVerdict = iota
	// SurfaceOrphan: on screen, but no longer inside the descent this surface
	// was opened for. What each kind does differs; the verdict does not.
	SurfaceOrphan
	SurfaceShow // still in that descent: track the content box
)

// SurfaceOf answers it for one surface. descentID is the pane frame the
// surface was opened for, not always the tile it shows: a live link's surface
// belongs to the link's target while the frame carries the link row, so
// passing the target's id would orphan it every frame.
func SurfaceOf(onScreen bool, paneContentID, descentID string) SurfaceVerdict {
	if !onScreen {
		return SurfacePark
	}
	if paneContentID == "" || paneContentID != descentID {
		return SurfaceOrphan
	}
	return SurfaceShow
}
