package nav

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/scratch"
)

// The world snapshot: gather then execute, the shape dragdrop.DropInput
// already established. Every impure read is resolved up front, so there is no
// App field and no js.Value inside this package and a field cleared
// mid-teardown can never be read late.
//
// Facts the machine PROJECTS rather than receives, because a snapshot field
// would be a copy: pane.FramingTarget, pane.FramingWriters, scratch.Ephemeral
// over PaneView.Scratch, pane.StillDescended, pane.ContentPanes, pane.TakeOver,
// OtherPaneShows, zoomtrans.*, shellconn.DecideAutoLive,
// textedit.DescentMode, urlwalk.Walk.
//
// Three-valued facts stay three-valued. ChildGridCached, DoorGridCached and
// scratch.Grid.Cached distinguish "no" from "not known yet", exactly as
// client/scratch requires. A snapshot never guesses.

// PaneView is one pane as navigation reads it.
type PaneView struct {
	ID string
	// Stack is a clone of the pane's place: the machine reads it and returns
	// a new one, and holds no place of its own between calls.
	Stack pane.Stack
	// Cx, Cy, Zoom are the pane's live viewport — the top frame's, unrolled,
	// and mid-animation the transition's scratch values.
	Cx, Cy, Zoom float64
	TextScrollX  float64
	TextScrollY  float64
	TextMode     string
	Rect         pane.Rect
	OnScreen     bool
	// GridID is the grid the place names: the anchor walked down the doorway
	// path. Resolved by the gatherer because the walk reads the cache and
	// kicks its own fetches.
	GridID string
	// Scratch is that grid as the scratch rule reads it — the stamp a visit
	// from this pane lands in, and whether it is known yet. It is a per-pane
	// fact, so it rides the pane rather than each verb's own half: the
	// ascent's ephemeral delete and the restore's heal ask the same question
	// and must not be able to disagree.
	Scratch scratch.Grid
}

// Notice is a resolved errsurface report — pluginhealth's wording for a
// doorway that cannot be entered, carried by value so the machine never asks
// the health classifier itself.
type Notice struct {
	Severity errsurface.Severity
	Source   string
	Message  string
}

// DoorWorld is the descent's extra half: everything about the doorway row
// that only the shim can resolve.
type DoorWorld struct {
	// DeadLink is deadref.DeadTile: the link points into a namespace this
	// node does not declare.
	DeadLink bool
	// IsLink is isLinkTile: the row's reference declaration, read by its one
	// owner and handed over as an answer.
	IsLink bool
	// ChildGridCached is whether the target grid is already in the cache.
	ChildGridCached bool
	// Health is pluginhealth.ClickNotice for a link with no child grid, and
	// nil when there is none — or when the question does not arise.
	Health *Notice
	// ReadOnly is tileReadOnly for the door row.
	ReadOnly bool
}

// LeaveWorld is the ascent's extra half: the frame being left, resolved
// against the row that owns it.
type LeaveWorld struct {
	// DescendedTile is descendedTile(p) for a content place — the cache-wide
	// walk that finds an off-grid ephemeral visit. nil when the row vanished
	// or was never cached.
	DescendedTile *rpc.Tile
	// DoorGridID is the grid the doorway row lives in, one level out.
	DoorGridID string
	// DoorGridCached says whether that grid is in the cache at all.
	DoorGridCached bool
	// DoorTile is the doorway row itself, nil when the grid holds none — a
	// + menu portal, for which the origin grid has no row.
	DoorTile *rpc.Tile
	// LandingView is the framing the grid being landed on was left at, from
	// the row that owns it (persistedGridView: the containing well for a
	// nested grid, the plugin's persisted root view for a root). nil when
	// nothing is persisted or the owning row is not cached.
	LandingView *Viewport
}

// RestoreTile is one cached row as the URL restore reads it: what the walk
// needs to classify it, and what a content leaf's frame is restored from.
type RestoreTile struct {
	ChildGridID string
	IsWell      bool
	IsContent   bool

	// The leaf's half: the mode a content descent restores in, and the scroll
	// it was left at. Only the trailing id can be a content leaf, but which id
	// that is only the walk knows, so every row carries them.
	TextDocument bool
	ReadOnly     bool
	TextY        int64
	TextMode     string
}

// RestoreWorld is the restore's extra half: the cache as the URL walk reads
// it.
//
// The whole cached set is projected, not a chosen subset, because which grids
// a path reaches is what the walk itself decides — and gather-then-execute
// admits no callback left open across the seam. Restores are boot and
// popstate only, so this is resolved a handful of times a session.
type RestoreWorld struct {
	// Grids is grid id → its rows.
	Grids map[string]map[string]RestoreTile
	// Failed is the grid-load latch: a grid the server already refused is not
	// asked again, and the walk stops there rather than suspending.
	Failed map[string]bool
	// RootViews is the framing each plugin root grid was left at, from the row
	// that owns it (persistedGridView's root arm), against the focused pane's
	// rect — the only pane a restore targets. Keyed by root grid id, because
	// which one the address names is the machine's to decode. A root with
	// nothing persisted is absent.
	RootViews map[string]Viewport
}

// grids answers the walk for one grid, and reports whether the snapshot holds
// it at all.
func (rw *RestoreWorld) rows(gridID string) (map[string]RestoreTile, bool) {
	if rw == nil {
		return nil, false
	}
	g, ok := rw.Grids[gridID]
	return g, ok
}

// failed reports the grid-load latch for gridID.
func (rw *RestoreWorld) failed(gridID string) bool {
	return rw != nil && rw.Failed[gridID]
}

// rootView is the persisted framing of a root grid, zero when there is none.
func (rw *RestoreWorld) rootView(gridID string) Viewport {
	if rw == nil {
		return Viewport{}
	}
	return rw.RootViews[gridID]
}

// World is one navigation snapshot.
type World struct {
	Focus string
	Panes []PaneView
	Home  string

	// CellPx is the renderer's base cell size at zoom 1.0; TransitionMs the
	// total wall-clock length of a descent or ascent; ZoomDistFactor the
	// log-zoom-to-pixel weighting the duration split uses; TextSideInset the
	// inner-box inset a content descent calibrates against. The four are the
	// renderer's constants, bound here rather than duplicated.
	CellPx         float64
	TransitionMs   float64
	ZoomDistFactor float64
	TextSideInset  float64

	// Animating is trans.Active per pane.
	Animating map[string]bool
	// MenuOpenOn is the pane the + menu is open on, "" for none.
	MenuOpenOn string
	Caps       caps.Caps
	// Surfaces is every pane holding a live url or shell surface, keyed by
	// the content it shows: the input to pane.TakeOver.
	Surfaces   []pane.Holder
	LevelDepth int
	LevelTop   *pane.Level

	// ShellAlive and ShellAliveKnown are the cached liveness probe results,
	// keyed by content id. Known is separate because "not probed yet" is not
	// "dead".
	ShellAlive      map[string]bool
	ShellAliveKnown map[string]bool

	// Door is set for a descent, Leave for an ascent hop, Restore for a URL
	// restore and every step of its walk.
	Door    *DoorWorld
	Leave   *LeaveWorld
	Restore *RestoreWorld
}

// Pane returns the pane view for id.
func (w World) Pane(id string) (PaneView, bool) {
	for _, p := range w.Panes {
		if p.ID == id {
			return p, true
		}
	}
	return PaneView{}, false
}

// otherPaneShows is pane.Tree.OtherPaneShows projected over the snapshot:
// some pane other than paneID is descended into tileID, so leaving does not
// delete it.
func (w World) otherPaneShows(paneID, tileID string) bool {
	for _, p := range w.Panes {
		if p.ID != paneID && p.Stack.ContentID() == tileID {
			return true
		}
	}
	return false
}
