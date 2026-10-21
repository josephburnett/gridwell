package pane

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// The window's stack of levels. What lies through a workspace tile is a whole tree,
// so it is the window that descends. The window-root tree is
// session-ephemeral and the durable home for a layout is the workspace tile: the
// server blob owns it, and savedHash here is only the persister's diff key.

// Level records one workspace tile descent.
type Level struct {
	// OuterTree is the pane tree this descent parked, restored verbatim on
	// ascent and alive while parked. nil when the workspace tile was entered from
	// boot; ascent then falls back to its containing grid.
	OuterTree  *Tree
	OriginPane string // the outer pane the descent happened in; focus returns there
	TileID     string // the workspace tile: owner of the layout blob, routing handle for its write
	// GridID is where a close-all ascent re-anchors when this level parked no
	// tree, and the glyph the bar's root crumb wears there.
	GridID string
	Name   string // the tile's label at descent time
	// ReadOnly latches when the layout blob could not be decoded, so the
	// session shows a default and never persists over what it could not read.
	ReadOnly bool
	// Basis is the version of the arrangement this tree derives from: what the
	// level's next layout write claims (see Levels.Claim).
	Basis     int64
	savedHash string // the persister's diff key over the bytes last sent
}

// TreeAtPlace is the single-pane tree a level falls back to. It is the one
// constructor for the decode-failure, boot-restore and capture-fallback
// defaults, so a level that could not read its blob and one that never had a
// blob open the same way.
func TreeAtPlace(idPrefix, anchor string, path []string, view rpc.View) *Tree {
	t := NewTree()
	t.IDPrefix = idPrefix
	p := t.FocusedPane()
	p.ID = idPrefix + p.ID
	t.Focus = p.ID
	p.Stack = StackAt(anchor, path, "")
	p.View = rpc.Saved(zoomtrans.Origin)
	if _, ok := view.Framing(); ok {
		p.View = view
	}
	p.Settle()
	return t
}

// PopEphemeralContent pops every leaf whose content descent ephemeral reports
// as ephemeral; it is asked only about a leaf in content. A captured
// arrangement is durable while an ephemeral descent dies with the visit that
// made it, so naming one would keep that visit's view alive past the boundary.
func PopEphemeralContent(t *Tree, ephemeral func(p *Pane, contentID string) bool) {
	if t == nil || ephemeral == nil {
		return
	}
	t.Walk(func(p *Pane) {
		id := p.ContentID()
		if id == "" {
			return
		}
		if ephemeral(p, id) {
			p.Pop()
		}
	})
}

// Levels is the window's nesting, bottom (first entered) to top (current).
type Levels struct {
	levels []Level
}

// Depth is how many workspace tiles deep the window is; 0 is the session tree.
func (s *Levels) Depth() int { return len(s.levels) }

func (s *Levels) Push(f Level) { s.levels = append(s.levels, f) }

// Pop leaves the current workspace tile, returning its level.
func (s *Levels) Pop() (Level, bool) {
	if len(s.levels) == 0 {
		return Level{}, false
	}
	f := s.levels[len(s.levels)-1]
	s.levels = s.levels[:len(s.levels)-1]
	return f, true
}

// Top returns the current level, mutable so the persister can update
// savedHash in place, or nil at depth 0.
func (s *Levels) Top() *Level {
	if len(s.levels) == 0 {
		return nil
	}
	return &s.levels[len(s.levels)-1]
}

// Claim is the version a layout write to tileID claims, read at send time
// because the write before it on the save chain may have just landed: the
// innermost open level's basis, or fallback once no level is open on it.
func (s *Levels) Claim(tileID string, fallback int64) int64 {
	for i := len(s.levels) - 1; i >= 0; i-- {
		if s.levels[i].TileID == tileID {
			return s.levels[i].Basis
		}
	}
	return fallback
}

// Landed records that this client's write to tileID claimed claimed and landed
// at landed. A level whose tree derived from the claim now derives from what
// landed; one on any other basis was not the writer's and keeps it, so its next
// write is refused rather than overwriting what it never read.
func (s *Levels) Landed(tileID string, claimed, landed int64) {
	for i := range s.levels {
		if f := &s.levels[i]; f.TileID == tileID && f.Basis == claimed {
			f.Basis = landed
		}
	}
}

// At returns the level at a 1-based nesting depth, mutable so the crumb
// rename can update Name in place, or nil when out of range.
func (s *Levels) At(level int) *Level {
	if level < 1 || level > len(s.levels) {
		return nil
	}
	return &s.levels[level-1]
}

// PopCountTo is how many levels to leave to be inside level k, level 0 being
// the session outside every workspace tile. Clicking the current boundary pops
// nothing.
func (s *Levels) PopCountTo(level int) int {
	if level < 0 || level >= len(s.levels) {
		return 0
	}
	return len(s.levels) - level
}

// tileIDs is the workspace tile of each level, outermost first.
func (s *Levels) tileIDs() []string {
	var out []string
	for _, f := range s.levels {
		out = append(out, f.TileID)
	}
	return out
}

func (s *Levels) Names() []string {
	out := make([]string, len(s.levels))
	for i, f := range s.levels {
		out[i] = f.Name
	}
	return out
}

func LayoutHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:8])
}

// ShouldPersist is the persister's single write decision: a writable level
// with bytes differing from the last saved. The codec is deterministic, so a
// visit that changed no arrangement writes nothing by construction.
func ShouldPersist(top *Level, encoded []byte) bool {
	if top == nil || top.ReadOnly || len(encoded) == 0 {
		return false
	}
	return LayoutHash(encoded) != top.savedHash
}

// MarkSaved records that encoded was sent, or was the descent-time baseline,
// so the next identical encode is a no-op. It is marked at send, not on the
// answer, because the layout's save chain lands its writes in order: the last
// one sent is the one the node will hold (see outbox.SaveQueue).
func MarkSaved(top *Level, encoded []byte) {
	if top == nil {
		return
	}
	top.savedHash = LayoutHash(encoded)
}

// ForgetSaved undoes MarkSaved for encoded once its write did not land, so the
// next flush sends it again. A mark for newer bytes stands.
func ForgetSaved(top *Level, encoded []byte) {
	if top == nil || top.savedHash != LayoutHash(encoded) {
		return
	}
	top.savedHash = ""
}

// NavCrumb is one link of the complete nav chain: the window's levels and the
// focused pane's frames end to end.
type NavCrumb struct {
	// Workspace marks a window-level boundary: WsLevel is the 1-based nesting
	// depth and TileID the workspace tile, the preview square and rename target.
	Workspace bool
	WsLevel   int
	TileID    string
	// Crumb is the live tree's focused pane's frame-stack entry; a click
	// ascends to it in place.
	Crumb Crumb
	// CloseOnly is the leading root crumb inside a workspace tile. Its click pops to
	// the session and is never an in-tree ascent.
	CloseOnly bool
}

// NavChain assembles the chain for pane p, outermost first. Parked trees' tile
// crumbs are deliberately absent, since clicking them would mutate a far-away
// tree's state from the bar.
func (s *Levels) NavChain(p *Pane) []NavCrumb {
	var out []NavCrumb
	depth := s.Depth()
	if depth > 0 {
		// The root crumb wears the glyph of wherever its click lands: the
		// session origin's root when this level parked a tree, otherwise the
		// grid its workspace tile sits in, where the close-all ascent re-anchors.
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
				out = append(out, NavCrumb{Workspace: true, WsLevel: k, TileID: f.TileID})
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
