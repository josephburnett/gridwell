// Package pane owns where the user is: the tmux-style pane tree, and each
// pane's place as one stack of frames (place.go).
//
// The place is one Stack, and it is the only owner of that fact. The URL
// (url.go), the layout blob (wire.go) and the bar's crumbs (chain.go) are
// encodings and projections of it. The window's nesting through pane tiles is
// Levels (levels.go), which speaks the same push/pop vocabulary.
//
// All logic here is pure Go: no syscall/js, no network. The package is
// imported by the wasm entry point and is fully covered by `go test`.
package pane

import (
	"errors"
	"fmt"
)

// Direction identifies a split orientation. "h" is a horizontal divider
// (top/bottom panes); "v" is vertical (left/right).
type Direction string

const (
	Horizontal Direction = "h"
	Vertical   Direction = "v"
)

// Side identifies one of a pane's four edges. Used by the input layer
// to translate a click position into a split orientation + which half
// the new pane should occupy.
type Side int

const (
	SideTop Side = iota
	SideBottom
	SideLeft
	SideRight
)

// Direction returns the split orientation for a side: top/bottom map to
// horizontal (top–bottom panes), left/right to vertical (left–right).
func (s Side) Direction() Direction {
	if s == SideTop || s == SideBottom {
		return Horizontal
	}
	return Vertical
}

// Pane is a leaf in the pane tree: one viewport. Its place — the grid it is
// in, the doorways it came through, the viewport at each of them, and any
// content descent — is the embedded Stack (place.go), the one owner of "where
// am I". The stack's top frame is unrolled into the pane, so p.Cx, p.Zoom,
// p.TextMode and friends read the pane's current level directly.
type Pane struct {
	ID string
	Stack
}

// Clone returns a deep copy of the pane, place included.
func (p *Pane) Clone(newID string) *Pane {
	c := *p
	c.ID = newID
	c.Stack = p.Stack.Clone()
	return &c
}

// Split is an internal tile in the pane tree. Ratio is in [0, 1]; A is the
// top/left child, B is the bottom/right.
type Split struct {
	Dir   Direction
	Ratio float64
	A     TreeNode
	B     TreeNode
}

// TreeNode is the sum type of pane-tree tiles. Exactly one of *Pane or *Split
// is non-nil. It is an explicit struct rather than an interface to keep JSON
// marshaling straightforward.
type TreeNode struct {
	Pane  *Pane
	Split *Split
}

// IsLeaf reports whether the tile is a leaf pane.
func (n TreeNode) IsLeaf() bool { return n.Pane != nil }

// Tree is the whole pane state, plus the id of the keyboard-focused pane.
type Tree struct {
	Root  TreeNode
	Focus string
	// Zoomed, when non-empty, names the leaf pane that temporarily owns the
	// whole layout (tmux-style zoom): Layout returns only it, Dividers
	// returns none, and the split ratios underneath stay untouched so
	// unzooming restores the exact prior arrangement. Structural edits
	// (Split, Swap, Collapse) unzoom first. Session-local view state, like
	// Focus.
	Zoomed string
	// nextID is incremented on each split to mint fresh pane ids.
	nextID int
	// IDPrefix namespaces every pane id this tree mints or decodes. Stacked
	// trees are alive simultaneously, and pane ids key the wasm locals, the
	// native view registry, and the shell streams, so the "w<level>:" prefix
	// keeps levels from colliding. Stored layout blobs stay bare:
	// EncodeLayout strips the prefix, DecodeLayout applies it.
	IDPrefix string
}

// ToggleZoom zooms paneID to the full layout, or unzooms if it is already
// the zoomed pane. Unknown ids are ignored.
func (t *Tree) ToggleZoom(paneID string) {
	if t.Zoomed == paneID {
		t.Zoomed = ""
		return
	}
	if t.FindPane(paneID) != nil {
		t.Zoomed = paneID
	}
}

// NewTree returns a fresh tree with a single pane at root.
func NewTree() *Tree {
	t := &Tree{nextID: 1}
	pane := &Pane{ID: "p1", Stack: NewStack("")}
	t.Root = TreeNode{Pane: pane}
	t.Focus = pane.ID
	return t
}

// Walk visits every leaf pane in tree order and calls fn for each.
func (t *Tree) Walk(fn func(*Pane)) {
	walk(t.Root, fn)
}

// WalkLeaves visits every leaf pane in the subtree rooted at n. Used to
// flush (save/freeze) the panes about to vanish when a split is collapsed.
func WalkLeaves(n TreeNode, fn func(*Pane)) {
	walk(n, fn)
}

func walk(n TreeNode, fn func(*Pane)) {
	if n.IsLeaf() {
		fn(n.Pane)
		return
	}
	walk(n.Split.A, fn)
	walk(n.Split.B, fn)
}

// FindPane returns the leaf with the given id, or nil.
func (t *Tree) FindPane(id string) *Pane {
	var found *Pane
	t.Walk(func(p *Pane) {
		if p.ID == id {
			found = p
		}
	})
	return found
}

// FocusedPane returns the focused pane, or nil if focus is invalid.
func (t *Tree) FocusedPane() *Pane { return t.FindPane(t.Focus) }

// SplitOnSideAt splits the focused pane such that the new pane occupies
// the requested side at the given ratio of the parent split, and moves
// focus to the new pane. The ratio is interpreted as "fraction of the
// parent split that the new pane consumes" — so e.g.
// SplitOnSideAt(SideTop, 0.3) makes the new pane the top 30%.
//
// Ratio is clamped to [0, 1]; values at the extremes still produce a
// valid split (just degenerate), so the caller is responsible for
// rejecting absurd ratios upstream.
//
// The new pane inherits the focused pane's place through Clone.
func (t *Tree) SplitOnSideAt(side Side, ratio float64) (*Pane, error) {
	newP, err := t.Split(side.Direction())
	if err != nil {
		return nil, err
	}
	split := findParentSplit(&t.Root, newP.ID)
	if split == nil {
		// Split just inserted one, so this is unreachable. Bail safely.
		t.Focus = newP.ID
		return newP, nil
	}
	if side == SideTop || side == SideLeft {
		// Tree.Split puts the existing pane in A and the new pane in B.
		// For "new pane on top/left" we swap them so the new pane is A,
		// and the requested ratio is the new pane's fraction.
		split.A, split.B = split.B, split.A
		split.Ratio = clamp01(ratio)
	} else {
		// New pane on bottom/right (B side): ratio is its fraction, so
		// the split's A-fraction is 1 - ratio.
		split.Ratio = 1 - clamp01(ratio)
	}
	t.Focus = newP.ID
	return newP, nil
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// findParentSplit returns the *Split whose direct child contains the
// pane with id targetID, or nil if not found.
func findParentSplit(n *TreeNode, targetID string) *Split {
	if n.IsLeaf() {
		return nil
	}
	if (n.Split.A.IsLeaf() && n.Split.A.Pane.ID == targetID) ||
		(n.Split.B.IsLeaf() && n.Split.B.Pane.ID == targetID) {
		return n.Split
	}
	if s := findParentSplit(&n.Split.A, targetID); s != nil {
		return s
	}
	return findParentSplit(&n.Split.B, targetID)
}

// Split splits the focused pane along dir at ratio 0.5. The new pane is a
// clone of the focused pane (same descent path, viewport, zoom). Returns
// the new pane.
func (t *Tree) Split(dir Direction) (*Pane, error) {
	t.Zoomed = "" // structural edits unzoom first (issue #80)
	focused := t.FocusedPane()
	if focused == nil {
		return nil, errors.New("no focused pane")
	}
	t.nextID++
	newPane := focused.Clone(fmt.Sprintf("%sp%d", t.IDPrefix, t.nextID))

	// Find the parent of the focused pane and replace it with a Split.
	var replaced bool
	t.Root, replaced = replacePane(t.Root, focused.ID, TreeNode{
		Split: &Split{
			Dir: dir, Ratio: 0.5,
			A: TreeNode{Pane: focused},
			B: TreeNode{Pane: newPane},
		},
	})
	if !replaced {
		return nil, errors.New("internal: focused pane not in tree")
	}
	return newPane, nil
}

// replacePane substitutes the leaf pane with id targetID for replacement.
// Returns the new tile and whether a replacement occurred.
func replacePane(n TreeNode, targetID string, replacement TreeNode) (TreeNode, bool) {
	if n.IsLeaf() {
		if n.Pane.ID == targetID {
			return replacement, true
		}
		return n, false
	}
	if a, ok := replacePane(n.Split.A, targetID, replacement); ok {
		n.Split.A = a
		return n, true
	}
	if b, ok := replacePane(n.Split.B, targetID, replacement); ok {
		n.Split.B = b
		return n, true
	}
	return n, false
}

func anyLeafID(n TreeNode) string {
	if n.IsLeaf() {
		return n.Pane.ID
	}
	if id := anyLeafID(n.Split.A); id != "" {
		return id
	}
	return anyLeafID(n.Split.B)
}

// Swap exchanges the positions of two panes in the tree. After Swap,
// the pane previously at idA's tree position now sits where idB was,
// and vice versa. Per-pane state (selection, animation) keyed by pane
// id travels with the pane content automatically.
//
// idA == idB is a no-op (returns nil). Either id missing returns an
// error and leaves the tree unchanged.
//
// Focus is not moved by Swap; the caller decides where focus goes
// after the swap based on the input gesture (e.g., release pane).
func (t *Tree) Swap(idA, idB string) error {
	if idA == idB {
		return nil
	}
	t.Zoomed = "" // structural edits unzoom first (issue #80)
	holderA := findPaneNode(&t.Root, idA)
	holderB := findPaneNode(&t.Root, idB)
	if holderA == nil || holderB == nil {
		return errors.New("pane not found")
	}
	*holderA, *holderB = *holderB, *holderA
	return nil
}

// findPaneNode returns a pointer to the *TreeNode slot in the tree that
// contains the leaf with id targetID, or nil if not found. Used by
// Swap to exchange positions without rebuilding the tree.
func findPaneNode(n *TreeNode, targetID string) *TreeNode {
	if n.IsLeaf() {
		if n.Pane.ID == targetID {
			return n
		}
		return nil
	}
	if hit := findPaneNode(&n.Split.A, targetID); hit != nil {
		return hit
	}
	return findPaneNode(&n.Split.B, targetID)
}

// SetFocus sets keyboard focus to the given pane id. Returns error if the
// id is unknown.
func (t *Tree) SetFocus(id string) error {
	if t.FindPane(id) == nil {
		return errors.New("pane not found")
	}
	t.Focus = id
	return nil
}

// StillDescended reports whether pane p (nil means closed) is still descended
// into tileID. It is the moved-on guard every async descent path applies
// after an await — a fetch, a probe, a target-row lookup: the pane may have
// closed, ascended, or descended elsewhere while the reply was in flight, and
// a late placement would leave a native surface over a pane that no longer
// shows that tile. Checking existence alone is not enough.
func StillDescended(p *Pane, tileID string) bool {
	return p != nil && p.ContentID() == tileID
}

// RelocateTo moves pane p to where dest stands — anchor, path, viewport — and
// descends it into tileID, whose footprint is foot and which is shown at
// zoom. This is the promote gesture: an ephemeral url visit is dragged from
// the bar onto another pane's grid and becomes a persistent tile there, and
// the visiting pane follows its content, so the nav chain and the next ascent
// both read the new place.
//
// The frame comes from ContentFrame, the same constructor a descent uses, so
// a promoted pane is in every way a descended pane.
func (p *Pane) RelocateTo(dest *Pane, tileID string, foot Footprint, zoom float64) {
	p.Stack = dest.Stack.Clone()
	if p.Content {
		// The destination is itself in a content descent: the promoted tile
		// replaces it rather than stacking on it (the pane follows its
		// content to where the tile now lives, one level deep).
		p.Pop()
	}
	p.Push(ContentFrame(tileID, foot, zoom, "", 0, 0))
}

// OtherPaneShows reports whether any leaf other than paneID is descended into
// tileID. It is the one guard on delete-on-ascent for an ephemeral tile:
// splitting an ephemeral visit clones the descent, and the clone's ascent, or
// its promotion onto a grid, must not delete the row the source pane still
// shows.
func (t *Tree) OtherPaneShows(paneID, tileID string) bool {
	found := false
	t.Walk(func(p *Pane) {
		if p.ID != paneID && p.ContentID() == tileID {
			found = true
		}
	})
	return found
}

// GridNotice is the one wording of a pane whose grid is not in the cache:
// the wait while a fetch is in flight (a plugin building its first
// listing can take a while), or the failure once the last fetch failed.
// name is the plugin's label when known, else the grid id.
func GridNotice(name string, failed bool) string {
	if failed {
		return name + " unavailable"
	}
	return "loading " + name + "…"
}
