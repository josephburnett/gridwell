package nav

import "github.com/josephburnett/gridwell/client/pane"

// A guard is what must still be true when an async answer lands. It is
// evaluated against the FRESH snapshot, so it is a projection of pane.Stack
// computed at resume and stored nowhere — not a second copy of "where is this
// pane", which a generation counter would be.
//
// The existing moved-on checks were never one rule. They are different
// preconditions per path: still descended in this tile; still sitting
// untouched at this anchor; still the top level. The closed set below is
// those checks, spelled once.
type GuardKind int

const (
	// GuardAlways holds unconditionally.
	GuardAlways GuardKind = iota
	// GuardPaneExists: the pane is still in the tree. PaneID.
	GuardPaneExists
	// GuardDescendedIn: the pane is still descended in this tile —
	// pane.StillDescended. PaneID, TileID.
	GuardDescendedIn
	// GuardPaneUntouched: the pane still sits at this anchor with nothing
	// pushed on it — fallbackTreeFor's re-centre guard. PaneID, Anchor.
	// (Phase C.)
	GuardPaneUntouched
	// GuardLevelTopIs: this pane tile is still the top level — the rename
	// and layout-flush guard. TileID. (Phase C.)
	GuardLevelTopIs
)

// Guard is one precondition.
type Guard struct {
	Kind   GuardKind
	PaneID string
	TileID string
	Anchor string
}

// holds evaluates the guard against a fresh snapshot. False retires the
// continuation and the plan is empty: that is the whole moved-on rule.
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
	case GuardLevelTopIs:
		return w.LevelTop != nil && w.LevelTop.TileID == g.TileID
	}
	return false
}
