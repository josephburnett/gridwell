package pane

import (
	"crypto/sha256"
	"encoding/hex"
)

// The WINDOW's stack of levels: which pane tile the user is inside
// (possibly nested), what outer tree each descent parked, and what layout
// bytes were last persisted.
//
// A pane tile is a doorway like any other, but what lies through it is a
// whole TREE of panes, not one pane's grid — so it is the window that
// descends, not a pane (owner decision #13: the window-root pane tree is
// session-ephemeral; the durable home for a layout is the pane tile). The
// vocabulary is the frame stack's: Push descends, Pop ascends, At projects
// a crumb. The persisted layout is owned by the server blob; savedHash here
// is only the persister's diff key, never a second copy of the layout.

// Level records one pane-tile descent.
type Level struct {
	// OuterTree is the pane tree this descent parked, restored verbatim on
	// ascent. It stays ALIVE while parked (#249): its views keep rendering
	// off-screen and its shells stay attached. nil when the workspace was
	// entered from boot (the w= URL) — ascent then falls back to the pane
	// tile's containing grid.
	OuterTree *Tree
	// OriginPane is the outer pane the descent happened in; focus returns
	// there on ascent.
	OriginPane string
	// TileID is the pane tile's qualified id — the owner of the layout blob
	// and the routing handle for the layout WriteContent.
	TileID string
	// Name is the tile's display label at descent time (the bar's crumb).
	Name string
	// ReadOnly latches when the layout blob could not be decoded (corrupt,
	// or written by a newer Gridwell): the session shows a default workspace
	// and must NEVER persist over the blob it couldn't read.
	ReadOnly bool
	// savedHash is the digest of the last-persisted (or descent-time) layout
	// bytes — the persister's diff key.
	savedHash string
}

// Levels is the window's nesting, bottom (first entered) to top (current).
type Levels struct {
	frames []Level
}

// Depth returns how many pane tiles deep the window is (0 = the session tree).
func (s *Levels) Depth() int { return len(s.frames) }

// Push enters a pane tile.
func (s *Levels) Push(f Level) { s.frames = append(s.frames, f) }

// Pop leaves the current pane tile, returning its level.
func (s *Levels) Pop() (Level, bool) {
	if len(s.frames) == 0 {
		return Level{}, false
	}
	f := s.frames[len(s.frames)-1]
	s.frames = s.frames[:len(s.frames)-1]
	return f, true
}

// Top returns the current level (mutable: the persister updates savedHash in
// place), or nil at depth 0.
func (s *Levels) Top() *Level {
	if len(s.frames) == 0 {
		return nil
	}
	return &s.frames[len(s.frames)-1]
}

// At returns the level at a 1-based nesting depth (leftmost crumb = 1),
// mutable (the crumb rename updates Name in place), or nil when out of
// range.
func (s *Levels) At(level int) *Level {
	if level < 1 || level > len(s.frames) {
		return nil
	}
	return &s.frames[level-1]
}

// PopCountTo maps a nav target to how many levels to leave: level k
// (1-based) means "be INSIDE level k" — pop everything deeper; level 0 is
// the session outside every pane tile. The one-chain nav (issue #245): a
// crumb click GOES THERE, so clicking the current boundary pops nothing
// (you are already there), and the old leave-k-inclusive semantics live at
// level k-1. 0 for an out-of-range level.
func (s *Levels) PopCountTo(level int) int {
	if level < 0 || level >= len(s.frames) {
		return 0
	}
	return len(s.frames) - level
}

// Names returns the breadcrumb labels, outermost first.
func (s *Levels) Names() []string {
	out := make([]string, len(s.frames))
	for i, f := range s.frames {
		out[i] = f.Name
	}
	return out
}

// LayoutHash digests encoded layout bytes for the persister's diff.
func LayoutHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:8])
}

// ShouldPersist is the persister's single write decision: write only when
// inside a writable level AND the encoded bytes differ from what was last
// saved. A pure visit (no arrangement change) produces identical bytes
// (the codec is deterministic) and never writes — "reading never mutates"
// holds by construction, with no per-call-site persistence hooks to forget.
func ShouldPersist(top *Level, encoded []byte) bool {
	if top == nil || top.ReadOnly || len(encoded) == 0 {
		return false
	}
	return LayoutHash(encoded) != top.savedHash
}

// MarkSaved records that encoded was durably written (or was the descent-time
// baseline), so the next identical encode is a no-op.
func MarkSaved(top *Level, encoded []byte) {
	if top == nil {
		return
	}
	top.savedHash = LayoutHash(encoded)
}

// NavCrumb is one link of the COMPLETE nav chain (issue #245): the whole
// path from the root in one breadcrumb — the window's levels and the
// focused pane's frames, projected end to end. Clicking any crumb GOES
// THERE; the last crumb is where you are, so it does nothing.
type NavCrumb struct {
	// PaneTile marks a window-level boundary: WsLevel is the 1-based
	// nesting depth, TileID the pane tile (preview square + rename target).
	PaneTile bool
	WsLevel  int
	TileID   string
	// Crumb is the frame-stack entry of the LIVE tree's focused pane
	// (parked trees' chains are not shown) — a click ascends to it in place.
	Crumb Crumb
	// CloseOnly: the leading ROOT crumb while inside a pane tile (owner
	// tweak 2026-08-04 on #245): its click CLOSES all of them — pop to the
	// session, never an in-tree ascent (mutating a far-away tree's state
	// from the bar read badly; the session restores exactly as left).
	CloseOnly bool
}

// NavChain assembles the chain for pane p, outermost first: the ROOT crumb
// (click = close all levels), one boundary crumb per open pane tile, then
// p's own frame stack. The parked trees' tile crumbs are deliberately NOT
// shown (owner tweak 2026-08-04 on #245): clicking them mutated a far-away
// tree's state from the bar, and the last of them duplicated "go to the
// previous view" — the boundary crumbs already say that. Outside every pane
// tile (Depth 0) the chain is just p's own.
func (s *Levels) NavChain(p *Pane) []NavCrumb {
	var out []NavCrumb
	depth := s.Depth()
	if depth > 0 {
		// The root crumb wears the session origin's ROOT face (namespace
		// glyph) when it is known; a boot-restored level has none and the
		// crumb draws as the muted placeholder. Either way the click only
		// closes levels.
		root := NavCrumb{CloseOnly: true}
		if f := s.At(1); f != nil && f.OuterTree != nil && f.OriginPane != "" {
			if op := f.OuterTree.FindPane(f.OriginPane); op != nil {
				if chain := op.Crumbs(); len(chain) > 0 {
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
		for _, c := range p.Crumbs() {
			out = append(out, NavCrumb{Crumb: c})
		}
	}
	return out
}
