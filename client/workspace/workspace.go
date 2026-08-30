// Package workspace owns the session-only workspace stack: which pane tile
// the user is inside (possibly nested), what outer tree each descent
// replaced, and what layout bytes were last persisted. Pure Go (no js), so
// the push/pop/fallback rules and the persister's write decision are
// headlessly tested.
//
// Ownership (charter §1): this stack is the ONE owner of "which tree is the
// user looking at" bookkeeping; the App's live *pane.Tree is the display of
// the top of it. The stack is session-only BY DESIGN — like portal Up
// frames, a reload loses the return trip (the URL's w= place restores the
// innermost workspace; ascent then falls back to the pane tile's containing
// grid). The persisted layout is owned by the server blob; SavedHash here is
// only the persister's diff key, never a second copy of the layout.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/josephburnett/gridwell/client/pane"
)

// Frame records one workspace descent: what to restore on ascent and how to
// persist the layout while inside.
type Frame struct {
	// OuterTree is the pane tree this descent replaced, restored verbatim on
	// ascent (its native handles get reload semantics: flushed and forgotten
	// at the boundary, per-pane ids collide between trees). nil when the
	// workspace was entered from boot (the w= URL) — ascent then falls back
	// to the pane tile's containing grid.
	OuterTree *pane.Tree
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

// Stack is the workspace nesting, bottom (first entered) to top (current).
type Stack struct {
	frames []Frame
}

// Depth returns how many workspaces deep the user is (0 = the session tree).
func (s *Stack) Depth() int { return len(s.frames) }

// Push enters a workspace.
func (s *Stack) Push(f Frame) { s.frames = append(s.frames, f) }

// Pop leaves the current workspace, returning its frame.
func (s *Stack) Pop() (Frame, bool) {
	if len(s.frames) == 0 {
		return Frame{}, false
	}
	f := s.frames[len(s.frames)-1]
	s.frames = s.frames[:len(s.frames)-1]
	return f, true
}

// Top returns the current workspace's frame (mutable: the persister updates
// savedHash in place), or nil at depth 0.
func (s *Stack) Top() *Frame {
	if len(s.frames) == 0 {
		return nil
	}
	return &s.frames[len(s.frames)-1]
}

// At returns the frame at a 1-based nesting level (leftmost crumb = 1),
// mutable (the crumb rename updates Name in place), or nil when out of
// range.
func (s *Stack) At(level int) *Frame {
	if level < 1 || level > len(s.frames) {
		return nil
	}
	return &s.frames[level-1]
}

// Names returns the breadcrumb labels, outermost first.
func (s *Stack) Names() []string {
	out := make([]string, len(s.frames))
	for i, f := range s.frames {
		out[i] = f.Name
	}
	return out
}

// PopCountTo maps a nav target to how many workspaces to leave: level k
// (1-based) means "be INSIDE workspace k" — pop everything deeper; level
// 0 is the session outside every workspace. The one-chain nav (issue
// #245): a crumb click GOES THERE, so clicking the current boundary pops
// nothing (you are already there), and the old leave-k-inclusive
// semantics live at level k-1. 0 for an out-of-range level.
func (s *Stack) PopCountTo(level int) int {
	if level < 0 || level >= len(s.frames) {
		return 0
	}
	return len(s.frames) - level
}

// LayoutHash digests encoded layout bytes for the persister's diff.
func LayoutHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:8])
}

// ShouldPersist is the persister's single write decision: write only when
// inside a writable workspace AND the encoded bytes differ from what was
// last saved. A pure visit (no arrangement change) produces identical bytes
// (the codec is deterministic) and never writes — "reading never mutates"
// holds by construction, with no per-call-site persistence hooks to forget.
func ShouldPersist(top *Frame, encoded []byte) bool {
	if top == nil || top.ReadOnly || len(encoded) == 0 {
		return false
	}
	return LayoutHash(encoded) != top.savedHash
}

// MarkSaved records that encoded was durably written (or was the descent-time
// baseline), so the next identical encode is a no-op.
func MarkSaved(top *Frame, encoded []byte) {
	if top == nil {
		return
	}
	top.savedHash = LayoutHash(encoded)
}
