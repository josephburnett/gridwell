package pane

import (
	"crypto/sha256"
	"encoding/hex"
)

// The window's stack of levels: which pane tile the user is inside (possibly
// nested), what outer tree each descent parked, and what layout bytes were
// last persisted.
//
// A pane tile is a doorway like any other, but what lies through it is a
// whole tree of panes, not one pane's grid, so it is the window that
// descends, not a pane. The window-root pane tree is session-ephemeral; the
// durable home for a layout is the pane tile. The vocabulary is the frame
// stack's: Push descends, Pop ascends, At projects a crumb. The persisted
// layout is owned by the server blob; savedHash here is only the persister's
// diff key, never a second copy of the layout.

// Level records one pane-tile descent.
type Level struct {
	// OuterTree is the pane tree this descent parked, restored verbatim on
	// ascent. It stays alive while parked: its views keep rendering
	// off-screen and its shells stay attached. nil when the pane tile was
	// entered from boot (the w= URL); ascent then falls back to the pane
	// tile's containing grid.
	OuterTree *Tree
	// OriginPane is the outer pane the descent happened in; focus returns
	// there on ascent.
	OriginPane string
	// TileID is the pane tile's qualified id — the owner of the layout blob
	// and the routing handle for the layout WriteContent.
	TileID string
	// GridID is the grid the pane tile sits in, from the row the descent
	// already read. It is where a close-all ascent re-anchors when this level
	// parked no tree, and so the face the bar's root crumb wears there.
	GridID string
	// Name is the tile's display label at descent time (the bar's crumb).
	Name string
	// ReadOnly latches when the layout blob could not be decoded (corrupt,
	// or written by a newer Gridwell): the session shows a default layout
	// and never persists over the blob it could not read.
	ReadOnly bool
	// savedHash is the digest of the last-persisted (or descent-time) layout
	// bytes — the persister's diff key.
	savedHash string
}

// TreeAtPlace builds the single-pane tree a level falls back to: one pane at
// the given place, under the level's own id prefix. It is the decode-failure
// default, the boot-restore default, and the capture fallback when the current
// tree cannot encode — one constructor, so a level that could not read its
// blob and a level that never had one open the same way.
func TreeAtPlace(idPrefix, anchor string, path []string, cx, cy, zoom float64) *Tree {
	t := NewTree()
	t.IDPrefix = idPrefix
	p := t.FocusedPane()
	p.ID = idPrefix + p.ID
	t.Focus = p.ID
	p.Stack = StackAt(anchor, path, "")
	p.Cx, p.Cy = cx, cy
	if zoom <= 0 {
		zoom = 1
	}
	p.Zoom = zoom
	return t
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

// PopCountTo maps a nav target to how many levels to leave: level k (1-based)
// means "be inside level k", so everything deeper pops; level 0 is the
// session outside every pane tile. A crumb click goes there, so clicking the
// current boundary pops nothing. 0 for an out-of-range level.
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
// inside a writable level and the encoded bytes differ from what was last
// saved. A pure visit with no arrangement change produces identical bytes,
// because the codec is deterministic, and never writes. Reading never
// mutates, by construction rather than by remembering a hook.
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

// NavCrumb is one link of the complete nav chain: the whole path from the
// root in one breadcrumb — the window's levels and the focused pane's frames,
// projected end to end. Clicking any crumb goes there; the last crumb is
// where you are, so it does nothing.
type NavCrumb struct {
	// PaneTile marks a window-level boundary: WsLevel is the 1-based
	// nesting depth, TileID the pane tile (preview square + rename target).
	PaneTile bool
	WsLevel  int
	TileID   string
	// Crumb is the frame-stack entry of the live tree's focused pane; parked
	// trees' chains are not shown. A click ascends to it in place.
	Crumb Crumb
	// CloseOnly: the leading root crumb while inside a pane tile. Its click
	// closes all of them — pop to the session, never an in-tree ascent, so
	// the bar never mutates a far-away tree's state.
	CloseOnly bool
}

// NavChain assembles the chain for pane p, outermost first: the root crumb
// (click closes all levels), one boundary crumb per open pane tile, then p's
// own frame stack. The parked trees' tile crumbs are deliberately not shown —
// clicking them would mutate a far-away tree's state from the bar, and the
// boundary crumbs already say "go to the previous view". Outside every pane
// tile (Depth 0) the chain is just p's own.
func (s *Levels) NavChain(p *Pane) []NavCrumb {
	var out []NavCrumb
	depth := s.Depth()
	if depth > 0 {
		// The root crumb wears the face of wherever its click lands: the
		// session origin's root when this level parked a tree, and otherwise
		// — a boot restore, which parked none — the grid the level's pane tile
		// sits in, which is where the close-all ascent re-anchors. Either way
		// the click only closes levels.
		root := NavCrumb{CloseOnly: true}
		if f := s.At(1); f != nil {
			if f.OuterTree != nil && f.OriginPane != "" {
				if op := f.OuterTree.FindPane(f.OriginPane); op != nil {
					if chain := op.Crumbs(); len(chain) > 0 {
						root.Crumb = chain[0]
					}
				}
			}
			if root.Crumb.Anchor == "" && root.Crumb.TileID == "" && f.GridID != "" {
				root.Crumb = Crumb{Anchor: f.GridID, ParentAnchor: f.GridID}
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
