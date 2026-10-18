package nav

import "github.com/josephburnett/gridwell/client/pane"

// A guard is what must still be true when an async answer lands, evaluated
// against the fresh snapshot: a projection of pane.Stack computed at resume
// and stored nowhere, rather than a second copy of where the pane is. The
// moved-on checks differ per path, and this is their closed set.
type GuardKind int

const (
	GuardAlways     GuardKind = iota // holds unconditionally
	GuardPaneExists                  // the pane is still in the tree: PaneID
	// GuardDescendedIn: the pane is still descended in this tile, by
	// pane.StillDescended. PaneID, TileID.
	GuardDescendedIn
	// GuardPaneUntouched: the pane still sits at this anchor with nothing
	// pushed on it, the post-reload level landing's re-centre guard. PaneID,
	// Anchor.
	GuardPaneUntouched
	// GuardLevelOpen: the window is still inside this workspace tile. TileID.
	GuardLevelOpen
)

// Guard is one precondition.
type Guard struct {
	Kind   GuardKind
	PaneID string
	TileID string
	Anchor string
}

// holds evaluates the guard against a fresh snapshot. False retires the
// continuation and the plan is empty.
func (g Guard) holds(w World) bool {
	switch g.Kind {
	case GuardAlways:
		return true
	case GuardPaneExists:
		_, ok := w.Pane(g.PaneID)
		return ok
	case GuardDescendedIn:
		p, ok := w.Pane(g.PaneID)
		return ok && pane.StillDescended(&pane.Pane{ID: p.ID, Stack: p.Stack}, g.TileID)
	case GuardPaneUntouched:
		p, ok := w.Pane(g.PaneID)
		return ok && p.Stack.Depth() == 1 && p.Stack.Anchor() == g.Anchor
	case GuardLevelOpen:
		return w.LevelTop != nil && w.LevelTop.TileID == g.TileID
	}
	return false
}
