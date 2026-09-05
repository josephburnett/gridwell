package nav

import "github.com/josephburnett/gridwell/api/rpc"

// GestureKind is the closed set of navigation verbs. Which frame a descent
// pushes is the doorway tile's declaration, never the call site's, so there
// is one Descend and one Ascend however the user reached them.
type GestureKind int

const (
	// GestureDescend takes a pane through a doorway: PaneID, Door.
	GestureDescend GestureKind = iota
	// GestureAscend leaves N levels of a pane's place: PaneID, N, Animate.
	GestureAscend
	// GestureRestore installs a place decoded from a URL: PaneID (empty =
	// the focused pane), Raw, Reset.
	GestureRestore
	// GestureRestoreFromHistory installs a whole session place: Raw.
	GestureRestoreFromHistory
	// GesturePromote turns an ephemeral visit into a persistent tile:
	// PaneID, DestPaneID, OldID, Created.
	GesturePromote
	// GestureEnterLevel descends the window into a pane tile: PaneID, Door.
	GestureEnterLevel
	// GestureLeaveLevels leaves pane-tile levels: Count.
	GestureLeaveLevels
	// GestureLandLevel finishes one leave hop against the tree the pop
	// installed: PaneID, TileID, Outer, Animate, Count.
	GestureLandLevel
	// GestureReEngage re-engages a restored content frame: PaneID, TileID.
	GestureReEngage
)

// Gesture is one navigation verb with its arguments. Like Effect it is a
// tagged struct: a continuation gesture (Plan.Next) is then plain data the
// shim hands straight back.
type Gesture struct {
	Kind GestureKind

	PaneID string
	// Door is the doorway row BY VALUE: an ephemeral scratch tile is in no
	// cached grid, so a lookup at transition end would miss it and the
	// descent would silently skip going live.
	Door rpc.Tile
	// N is how many levels an ascent leaves; Animate asks for the zoom-out
	// on the last hop.
	N       int
	Animate bool

	Raw string
	// Reset asks a restore for the popstate half: the per-pane teardown a
	// reload would do, and the address handed back to the browser at the end.
	// A boot restore lands over whatever the pane already shows.
	Reset      bool
	DestPaneID string
	OldID      string
	Created    rpc.Tile
	TileID     string
	Count      int
	// Outer says the level just popped had parked a tree: its landing is the
	// return animation onto the pane tile, rather than the post-reload
	// fallback's re-centre. It is read at pop time, since by landing time the
	// level is gone.
	Outer bool
}
