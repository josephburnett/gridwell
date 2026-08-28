package workspace

import "github.com/josephburnett/gridwell/client/pane"

// NavCrumb is one link of the COMPLETE nav chain (issue #245): the whole
// path from the root in one breadcrumb. Each workspace frame contributes
// itself as a boundary crumb; the current tree's focused-pane chain ends
// it. Clicking any crumb GOES THERE — the last crumb is where you are, so
// it does nothing.
type NavCrumb struct {
	// PaneTile marks a workspace boundary: WsLevel is the 1-based stack
	// level, TileID the pane tile (preview square + rename target).
	PaneTile bool
	WsLevel  int
	TileID   string
	// Chain crumbs: Crumb is the descent-chain entry of the LIVE tree's
	// focused pane (intermediate trees' chains are not shown) — a click
	// ascends to it in place.
	Crumb pane.Crumb
	// CloseOnly: the leading ROOT crumb while inside a view (owner tweak
	// 2026-08-04 on #245): its click CLOSES all views — pop to the
	// session, never an in-tree ascent (mutating a far-away tree's state
	// from the bar read badly; the session restores exactly as left).
	CloseOnly bool
}

// NavChain assembles the chain for pane p, outermost first: the ROOT crumb
// (click = close all views), one boundary crumb per open view, then p's
// full descent chain. The intermediate trees' tile crumbs are deliberately
// NOT shown (owner tweak 2026-08-04 on #245): clicking them mutated a
// far-away tree's state from the bar, and the last of them duplicated
// "go to the previous view" — the boundary crumbs already say that.
// Outside every view (Depth 0) the chain is just p's own.
func (s *Stack) NavChain(p *pane.Pane) []NavCrumb {
	var out []NavCrumb
	depth := s.Depth()
	if depth > 0 {
		// The root crumb wears the session origin's ROOT face (namespace
		// glyph) when it is known; a boot-restored frame has none and the
		// crumb draws as the muted placeholder. Either way the click only
		// closes views.
		root := NavCrumb{CloseOnly: true}
		if f := s.At(1); f != nil && f.OuterTree != nil && f.OriginPane != "" {
			if op := f.OuterTree.FindPane(f.OriginPane); op != nil {
				if chain := pane.DescentChain(op); len(chain) > 0 {
					root.Crumb = chain[0]
				}
			}
		}
		out = append(out, root)
		for k := 1; k <= depth; k++ {
			if f := s.At(k); f != nil {
				out = append(out, NavCrumb{PaneTile: true, WsLevel: k, TileID: f.TileID})
			}
		}
	}
	if p != nil {
		for _, c := range pane.DescentChain(p) {
			out = append(out, NavCrumb{Crumb: c})
		}
	}
	return out
}
