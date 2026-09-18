package nav

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/scratch"
)

// The world snapshot: gather then execute. Every impure read is resolved up
// front, so this package holds no App field and no js.Value. What the machine
// can derive it projects rather than receives, since a snapshot field would be
// a copy. Three-valued facts stay three-valued: not known yet is not no.

// PaneView is one pane as navigation reads it.
type PaneView struct {
	ID string
	// Stack is a clone: the machine reads it and returns a new one, holding no
	// place of its own between calls.
	Stack pane.Stack
	// Cx, Cy, Zoom are the live viewport, the transition's scratch values
	// mid-animation.
	Cx, Cy, Zoom float64
	Rect         pane.Rect
	// GridID is the grid the place names, resolved by the gatherer because the
	// walk reads the cache and kicks its own fetches.
	GridID string
	// Scratch rides the pane rather than each verb's own half, so the ascent's
	// ephemeral delete and the restore's heal cannot disagree.
	Scratch scratch.Grid
}

// Notice is a resolved errsurface report, carried by value so the machine
// never asks the health classifier itself.
type Notice struct {
	Severity errsurface.Severity
	Source   string
	Message  string
}

// DoorWorld is what only the shim can resolve about the doorway row.
type DoorWorld struct {
	DeadLink bool // deadref.DeadTile
	IsLink   bool // isLinkTile, the row's reference declaration
	// Health is pluginhealth.ClickNotice for a link with no child grid, nil
	// when there is none or the question does not arise.
	Health   *Notice
	ReadOnly bool // tileReadOnly for the door row
}

// LeaveWorld is the frame being left, resolved against the row that owns it.
type LeaveWorld struct {
	// DescendedTile is the cache-wide walk that finds an off-grid ephemeral
	// visit, nil when the row vanished or was never cached.
	DescendedTile  *gridwellv1.Tile
	DoorGridID     string // the grid the doorway row lives in, one level out
	DoorGridCached bool
	// DoorTile is the doorway row, nil when the grid holds none, as for a +
	// menu portal.
	DoorTile *gridwellv1.Tile
	// LandingView is persistedGridView for the grid being landed on, nil when
	// nothing is persisted or the owning row is not cached.
	LandingView *Viewport
}

// LevelWorld is the row the return animation zooms out onto. nil means it is
// not cached and the landing is instant.
type LevelWorld struct {
	Tile *gridwellv1.Tile
}

// PromoteWorld is the ephemeral row being promoted away from, as the cache
// last held it. nil leaves it undeleted rather than guessing.
type PromoteWorld struct {
	OldTile *gridwellv1.Tile
}

func (pw *PromoteWorld) old() *gridwellv1.Tile {
	if pw == nil {
		return nil
	}
	return pw.OldTile
}

// RestoreTile is one cached row as the URL restore reads it.
type RestoreTile struct {
	ChildGridID string
	IsWell      bool
	IsContent   bool

	// The leaf's half. Only the trailing id can be a content leaf, but only
	// the walk knows which that is, so every row carries it.
	TextDocument bool
	ReadOnly     bool
	TextY        int64
	TextMode     string
}

// RestoreWorld is the cache as the URL walk reads it. The whole cached set is
// projected because which grids a path reaches is what the walk decides, and
// gather-then-execute admits no callback across the seam.
type RestoreWorld struct {
	Grids map[string]map[string]RestoreTile
	// Failed is the grid-load latch: a grid the server already refused is not
	// asked again, and the walk stops there rather than suspending.
	Failed map[string]bool
	// RootViews is persistedGridView's root arm against the focused pane's
	// rect, keyed by grid id because which one the address names is the
	// machine's to decode.
	RootViews map[string]Viewport
}

func (rw *RestoreWorld) rows(gridID string) (map[string]RestoreTile, bool) {
	if rw == nil {
		return nil, false
	}
	g, ok := rw.Grids[gridID]
	return g, ok
}

func (rw *RestoreWorld) failed(gridID string) bool {
	return rw != nil && rw.Failed[gridID]
}

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

	// The renderer's constants, bound here rather than duplicated: cell size
	// at zoom 1, the length of a descent, the log-zoom-to-pixel weighting of
	// the duration split, and the inner-box inset a content descent
	// calibrates against.
	CellPx         float64
	TransitionMs   float64
	ZoomDistFactor float64
	TextSideInset  float64

	Animating  map[string]bool // trans.Active per pane
	MenuOpenOn string          // the pane the + menu is open on, "" for none
	Caps       caps.Caps
	LevelDepth int
	LevelTop   *pane.Level

	// The cached liveness probe results, keyed by content id. Known is
	// separate because not probed yet is not dead.
	ShellAlive      map[string]bool
	ShellAliveKnown map[string]bool

	// One per verb: a descent, an ascent hop, a restore and every step of its
	// walk, a pane-tile landing, a promote.
	Door    *DoorWorld
	Leave   *LeaveWorld
	Restore *RestoreWorld
	Level   *LevelWorld
	Promote *PromoteWorld
}

func (w World) Pane(id string) (PaneView, bool) {
	for _, p := range w.Panes {
		if p.ID == id {
			return p, true
		}
	}
	return PaneView{}, false
}

// otherPaneShows reports that another pane is descended into tileID, so
// leaving does not delete it: splitting an ephemeral visit clones the descent,
// and the clone's ascent must not delete the row the source pane still shows.
func (w World) otherPaneShows(paneID, tileID string) bool {
	for _, p := range w.Panes {
		if p.ID != paneID && p.Stack.ContentID() == tileID {
			return true
		}
	}
	return false
}
