package nav

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// GestureKind is the closed set of navigation verbs. Which frame a descent
// pushes is the doorway tile's declaration, never the call site's, so there is
// one Descend and one Ascend however the user reached them.
type GestureKind int

const (
	GestureDescend GestureKind = iota // takes a pane through a doorway: PaneID, Door
	GestureAscend                     // leaves N levels of a place: PaneID, N, Animate
	// GestureRestore installs a place decoded from a URL: PaneID ("" = the
	// focused pane), Raw, Reset.
	GestureRestore
	GestureRestoreFromHistory // installs a whole session place: Raw
	// GesturePromote turns an ephemeral visit into a persistent tile: PaneID,
	// DestPaneID, OldID, Created.
	GesturePromote
	GestureEnterLevel  // descends the window into a pane tile: PaneID, Door
	GestureLeaveLevels // Count
	// GestureLandLevel finishes one leave hop against the tree the pop
	// installed: PaneID, TileID, Outer, Animate, Count.
	GestureLandLevel
	GestureReEngage   // re-engages a restored content frame: PaneID, TileID
	GestureFollowLink // places a live url view on a link's target: PaneID, Door
	// GestureEngageLeaves settles a freshly installed pane tree: every leaf's
	// grid asked for unless held current, every content leaf re-engaged.
	GestureEngageLeaves
)

// Gesture is one navigation verb with its arguments. Like Effect it is a
// tagged struct, so a continuation gesture is plain data the shim hands back.
type Gesture struct {
	Kind GestureKind

	PaneID string
	// Door is the doorway row by value: an ephemeral scratch tile is in no
	// cached grid, so a lookup at transition end would miss it and the
	// descent would silently skip going live.
	Door *gridwellv1.Tile
	// N is how many levels an ascent leaves; Animate asks for the zoom-out
	// on the last hop.
	N       int
	Animate bool

	Raw string
	// Reset asks a restore for the popstate half: the per-pane teardown a
	// reload would do, and the address handed back to the browser. A boot
	// restore lands over whatever the pane already shows.
	Reset      bool
	DestPaneID string
	OldID      string
	Created    *gridwellv1.Tile
	TileID     string
	Count      int
	// Outer says the level just popped had parked a tree, so its landing is
	// the return animation rather than the post-reload re-centre. Read at pop
	// time, since by landing time the level is gone.
	Outer bool
}
