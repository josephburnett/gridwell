// Package pane owns where the user is: the pane tree, and each pane's place as
// one Stack of frames. The Stack is the only owner of that fact; the URL, the
// layout blob and the bar's crumbs encode and project it. All of it is pure
// Go, so `go test` covers what the wasm entry point imports.
package pane

import (
	"errors"
	"fmt"
)

// Direction is a split orientation: "h" is a horizontal divider with top and
// bottom panes, "v" is vertical with left and right.
type Direction string

const (
	Horizontal Direction = "h"
	Vertical   Direction = "v"
)

// Side is one of a pane's four edges: a click becomes a split orientation and
// which half the new pane occupies.
type Side int

const (
	SideTop Side = iota
	SideBottom
	SideLeft
	SideRight
)

// Direction is the split orientation for a side.
func (s Side) Direction() Direction {
	if s == SideTop || s == SideBottom {
		return Horizontal
	}
	return Vertical
}

// Pane is a leaf in the pane tree. Its place is the embedded Stack, whose top
// frame is unrolled so p.Cx and friends read the current level directly.
type Pane struct {
	ID string
	Stack
}

func (p *Pane) Clone(newID string) *Pane {
	c := *p
	c.ID = newID
	c.Stack = p.Stack.Clone()
	return &c
}

// Split is an internal tile. Ratio is in [0, 1], A is the top or left child.
type Split struct {
	Dir   Direction
	Ratio float64
	A     TreeNode
	B     TreeNode
}

// TreeNode is the sum type of pane-tree tiles: exactly one field is non-nil.
// It is a struct rather than an interface to keep JSON marshaling simple.
type TreeNode struct {
	Pane  *Pane
	Split *Split
}

func (n TreeNode) IsLeaf() bool { return n.Pane != nil }

// Tree is the whole pane state, plus the id of the keyboard-focused pane.
type Tree struct {
	Root  TreeNode
	Focus string
	// Zoomed names the leaf pane that temporarily owns the whole layout. The
	// split ratios underneath stay untouched, so unzooming restores the exact
	// prior arrangement, and structural edits unzoom first. Session-local,
	// like Focus.
	Zoomed string
	nextID int
	// IDPrefix namespaces every pane id this tree mints or decodes, keeping
	// simultaneously-alive levels from colliding in the pane-keyed maps.
	// Stored blobs stay bare: EncodeLayout strips it, DecodeLayout applies it.
	IDPrefix string
}

// ToggleZoom zooms paneID to the full layout, or unzooms it. Unknown ids are
// ignored.
func (t *Tree) ToggleZoom(paneID string) {
	if t.Zoomed == paneID {
		t.Zoomed = ""
		return
	}
	if t.FindPane(paneID) != nil {
		t.Zoomed = paneID
	}
}

func NewTree() *Tree {
	t := &Tree{nextID: 1}
	pane := &Pane{ID: "p1", Stack: NewStack("")}
	t.Root = TreeNode{Pane: pane}
	t.Focus = pane.ID
	return t
}

// Walk visits every leaf pane in tree order.
func (t *Tree) Walk(fn func(*Pane)) {
	walk(t.Root, fn)
}

// WalkLeaves visits every leaf pane under n, for flushing the panes about to
// vanish when a split is collapsed.
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

func (t *Tree) FindPane(id string) *Pane {
	var found *Pane
	t.Walk(func(p *Pane) {
		if p.ID == id {
			found = p
		}
	})
	return found
}

func (t *Tree) FocusedPane() *Pane { return t.FindPane(t.Focus) }

// SplitOnSideAt splits the focused pane so the new one occupies side at ratio,
// the fraction of the parent split it consumes, and focuses it. Ratio is
// clamped, and an extreme still produces a valid, degenerate split, so
// rejecting absurd ratios is the caller's.
func (t *Tree) SplitOnSideAt(side Side, ratio float64) (*Pane, error) {
	newP, err := t.Split(side.Direction())
	if err != nil {
		return nil, err
	}
	split := findParentSplit(&t.Root, newP.ID)
	if split == nil {
		// Split just inserted one, so this is unreachable.
		t.Focus = newP.ID
		return newP, nil
	}
	if side == SideTop || side == SideLeft {
		// Split puts the existing pane in A, so a new pane on top or left
		// swaps them and takes the ratio directly.
		split.A, split.B = split.B, split.A
		split.Ratio = clamp01(ratio)
	} else {
		// On the B side the split's A-fraction is 1 - ratio.
		split.Ratio = 1 - clamp01(ratio)
	}
	t.Focus = newP.ID
	return newP, nil
}

func clamp01(x float64) float64 { return min(max(x, 0), 1) }

// findParentSplit is the Split whose direct child is targetID.
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
// clone, place included.
func (t *Tree) Split(dir Direction) (*Pane, error) {
	t.Zoomed = "" // structural edits unzoom first
	focused := t.FocusedPane()
	if focused == nil {
		return nil, errors.New("no focused pane")
	}
	t.nextID++
	newPane := focused.Clone(fmt.Sprintf("%sp%d", t.IDPrefix, t.nextID))

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

// replacePane substitutes targetID's leaf for replacement.
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

// Swap exchanges two panes' positions, so per-pane state keyed by pane id
// travels with the content. Focus does not move: where it goes is the input
// gesture's decision.
func (t *Tree) Swap(idA, idB string) error {
	if idA == idB {
		return nil
	}
	t.Zoomed = "" // structural edits unzoom first
	holderA := findPaneNode(&t.Root, idA)
	holderB := findPaneNode(&t.Root, idB)
	if holderA == nil || holderB == nil {
		return errors.New("pane not found")
	}
	*holderA, *holderB = *holderB, *holderA
	return nil
}

// findPaneNode is the TreeNode slot holding targetID's leaf, so Swap can
// exchange positions without rebuilding the tree.
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

// SetFocus moves keyboard focus, erroring on an unknown id.
func (t *Tree) SetFocus(id string) error {
	if t.FindPane(id) == nil {
		return errors.New("pane not found")
	}
	t.Focus = id
	return nil
}

// SplitBelowForOpen is the one programmatic split: a link opened out of a
// live tile and a ctrl-click descent both land in a new pane below the
// focused one, at half its height. target is that pane, or the focused pane
// itself when it is too short for two minimum panes, so an open never lands
// nowhere. shedContent reports that the new pane cloned a content level: one
// live surface per tile means the clone cannot keep the descent, so the
// caller ascends it before opening.
func (t *Tree) SplitBelowForOpen(r Rect) (target *Pane, split, shedContent bool) {
	focused := t.FocusedPane()
	if !CanSplit(SideBottom, r) {
		return focused, false, false
	}
	newP, err := t.SplitOnSideAt(SideBottom, 0.5)
	if err != nil {
		return focused, false, false
	}
	return newP, true, newP.ContentID() != ""
}

// StillDescended is the moved-on guard every async descent path applies after
// an await; nil p means closed. Existence alone is not enough, because a late
// placement would leave a native surface over a pane that moved.
func StillDescended(p *Pane, tileID string) bool {
	return p != nil && p.ContentID() == tileID
}

// RelocateTo moves pane p to where dest stands and descends it into tileID, so
// the nav chain and the next ascent read the new place. The frame comes from
// ContentFrame, so a promoted pane is in every way a descended pane.
func (p *Pane) RelocateTo(dest *Pane, tileID string, foot Footprint, zoom float64) {
	p.Stack = dest.Stack.Clone()
	if p.Content {
		// A destination already in a content descent is replaced rather than
		// stacked on: the pane follows its content, one level deep.
		p.Pop()
	}
	p.Push(ContentFrame(tileID, foot, zoom, "", 0, 0))
}

// GridNotice is the one wording for a pane whose grid is not cached: the wait
// while a fetch is in flight, or the failure once one failed. name is the
// plugin's label when known, else the grid id.
func GridNotice(name string, failed bool) string {
	if failed {
		return name + " unavailable"
	}
	return "loading " + name + "…"
}
