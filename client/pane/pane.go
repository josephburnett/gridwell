// Package pane implements the tmux-style pane tree used by the Gridwell client.
//
// All logic here is pure Go: no syscall/js, no network. The package is
// imported by the WASM entry point and is fully covered by standard
// `go test`.
package pane

import (
	"errors"
	"fmt"
	"slices"
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

// Pane is a leaf in the pane tree: one viewport. Path is the descent path
// (well row ids) from root to the currently-viewed grid. Cx, Cy are the
// viewport center in cells; Zoom is the pane's zoom multiplier.
//
// TextFocus, when nonzero, marks the pane as "descended into" a text tile:
// the pane still sits in the parent grid (Path is unchanged), but the
// chrome and input semantics switch to text-editing mode. TextMode picks
// between "text" (raw markdown in a textarea overlay) and "rendered" (the
// markdown layout rendered into the canvas). TextScrollY is the vertical
// scroll inside the text's interior in logical pixels; mirrored to the
// text tile's view_y on save.
type Pane struct {
	ID string
	// Anchor is the qualified grid id of the plugin root this pane is currently
	// inside; Path's well ids are relative to it. Anchor == "" means the pane is
	// at the launcher start screen (no grid, no descent).
	Anchor string `json:"anchor,omitempty"`
	Path   []string
	Cx     float64
	Cy     float64
	Zoom   float64

	TextFocus   string  `json:"file_focus,omitempty"`
	TextMode    string  `json:"file_mode,omitempty"`
	TextScrollX float64 `json:"file_scroll_x,omitempty"`
	TextScrollY float64 `json:"file_scroll_y,omitempty"`
	// TextZoom is the rendering scale for text-mode (independent of
	// parent-grid Zoom). 1.0 means "natural reading size"; the wheel
	// adjusts this directly when TextFocus != 0.
	TextZoom float64 `json:"file_zoom,omitempty"`

	// Up is the portal ascent stack: each frame is the pane state at a previous
	// plugin level, restored on ascent when Path is empty. Entering a plugin
	// (a portal jump that crosses plugin boundaries without a well tile in the
	// current grid) pushes the current level here.
	Up []Frame `json:"up,omitempty"`
}

// Frame snapshots a pane's per-plugin navigation level so a portal ascent can
// restore the exact level the user jumped from.
type Frame struct {
	Anchor      string   `json:"anchor,omitempty"`
	Path        []string `json:"path,omitempty"`
	Cx          float64  `json:"cx,omitempty"`
	Cy          float64  `json:"cy,omitempty"`
	Zoom        float64  `json:"zoom,omitempty"`
	TextFocus   string   `json:"tf,omitempty"`
	TextMode    string   `json:"tm,omitempty"`
	TextScrollX float64  `json:"tsx,omitempty"`
	TextScrollY float64  `json:"tsy,omitempty"`
	TextZoom    float64  `json:"tz,omitempty"`
	// MenuOpen records whether the + menu was open on this pane when the user
	// entered a plugin from it, so ascending back restores it — you come back
	// with the menu open, just as you left it.
	MenuOpen bool `json:"menu_open,omitempty"`
}

// Clone returns a deep copy of the pane (including the path + Up slices).
func (p *Pane) Clone(newID string) *Pane {
	c := *p
	c.ID = newID
	if len(p.Path) > 0 {
		c.Path = slices.Clone(p.Path)
	}
	if len(p.Up) > 0 {
		c.Up = slices.Clone(p.Up)
		for i := range c.Up {
			c.Up[i].Path = slices.Clone(p.Up[i].Path)
		}
	}
	return &c
}

// PushFrame snapshots the pane's current level onto the Up stack (called when
// entering a plugin). menuOpen records whether the + menu was open on the pane
// at that moment, so a later ascent can reopen it.
func (p *Pane) PushFrame(menuOpen bool) {
	p.Up = append(p.Up, Frame{
		Anchor: p.Anchor, Path: slices.Clone(p.Path),
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom,
		TextFocus: p.TextFocus, TextMode: p.TextMode,
		TextScrollX: p.TextScrollX, TextScrollY: p.TextScrollY, TextZoom: p.TextZoom,
		MenuOpen: menuOpen,
	})
}

// TopFrame returns the most recent Up frame (the level a portal ascent would
// return to), or false when the stack is empty.
func (p *Pane) TopFrame() (Frame, bool) {
	if len(p.Up) == 0 {
		return Frame{}, false
	}
	return p.Up[len(p.Up)-1], true
}

// PopFrame restores the most recent Up frame into the pane and returns true.
// Returns false when the stack is empty.
func (p *Pane) PopFrame() bool {
	if len(p.Up) == 0 {
		return false
	}
	f := p.Up[len(p.Up)-1]
	p.Up = p.Up[:len(p.Up)-1]
	p.Anchor, p.Path = f.Anchor, f.Path
	p.Cx, p.Cy, p.Zoom = f.Cx, f.Cy, f.Zoom
	p.TextFocus, p.TextMode = f.TextFocus, f.TextMode
	p.TextScrollX, p.TextScrollY, p.TextZoom = f.TextScrollX, f.TextScrollY, f.TextZoom
	return true
}

// DropFrame removes the most recent Up frame without applying it, returning
// true on success. Used by an animated portal ascent: the transition itself
// drives the pane back to the frame's viewport and anchor, so the frame is
// dropped from the stack but its values are restored by the animation rather
// than instantly (unlike PopFrame).
func (p *Pane) DropFrame() bool {
	if len(p.Up) == 0 {
		return false
	}
	p.Up = p.Up[:len(p.Up)-1]
	return true
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
// is non-nil. We model it explicitly rather than as an interface to keep
// JSON marshaling straightforward.
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
	// nextID is incremented on each split to mint fresh pane ids.
	nextID int
}

// NewTree returns a fresh tree with a single pane at root.
func NewTree() *Tree {
	t := &Tree{nextID: 1}
	pane := &Pane{ID: "p1", Zoom: 1.0}
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

// Count returns the number of leaf panes.
func (t *Tree) Count() int {
	n := 0
	t.Walk(func(*Pane) { n++ })
	return n
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
// New pane inherits the focused pane's path/viewport/file-mode state
// via Clone.
func (t *Tree) SplitOnSideAt(side Side, ratio float64) (*Pane, error) {
	newP, err := t.Split(side.Direction())
	if err != nil {
		return nil, err
	}
	split := findParentSplit(&t.Root, newP.ID)
	if split == nil {
		// Shouldn't happen: Split just inserted one. Bail safely.
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
	focused := t.FocusedPane()
	if focused == nil {
		return nil, errors.New("no focused pane")
	}
	t.nextID++
	newPane := focused.Clone(fmt.Sprintf("p%d", t.nextID))

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

// Close removes the focused pane. The focused pane's sibling is hoisted up
// to take the parent's place. If only one pane remains, Close returns an
// error per spec ("the last pane cannot be closed").
func (t *Tree) Close() error {
	if t.Count() == 1 {
		return errors.New("cannot close the last pane")
	}
	id := t.Focus
	newRoot, sibling, ok := dropPane(t.Root, id)
	if !ok {
		return errors.New("focus not in tree")
	}
	t.Root = newRoot
	// Move focus to the surviving sibling (or whatever leaf is closest).
	if sibling != "" {
		t.Focus = sibling
	} else {
		// Should not happen: dropPane always reports a sibling unless the
		// drop targeted root, which Count==1 would already have caught.
		t.Walk(func(p *Pane) { t.Focus = p.ID })
	}
	return nil
}

// dropPane returns the modified tree, the id of a pane to focus next (the
// surviving sibling), and ok=true if the target was found.
func dropPane(n TreeNode, targetID string) (TreeNode, string, bool) {
	if n.IsLeaf() {
		return n, "", false
	}
	// If A is the target leaf, B replaces this split.
	if n.Split.A.IsLeaf() && n.Split.A.Pane.ID == targetID {
		return n.Split.B, anyLeafID(n.Split.B), true
	}
	if n.Split.B.IsLeaf() && n.Split.B.Pane.ID == targetID {
		return n.Split.A, anyLeafID(n.Split.A), true
	}
	if a, sib, ok := dropPane(n.Split.A, targetID); ok {
		n.Split.A = a
		return n, sib, true
	}
	if b, sib, ok := dropPane(n.Split.B, targetID); ok {
		n.Split.B = b
		return n, sib, true
	}
	return n, "", false
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

// CollapseSplit removes one child of the given split, hoisting the
// surviving child into the split's slot. If dropA is true, the A child
// (and its subtree) is dropped and B replaces the split; otherwise A
// replaces it. Returns an error if the split is not in the tree.
//
// After collapse, focus moves to some leaf inside the surviving
// subtree (the previously-focused pane may have been the one removed).
//
// Used by the input layer when a right-drag squeezes one side of a
// divider to zero.
func (t *Tree) CollapseSplit(s *Split, dropA bool) error {
	holder := findSplitHolder(&t.Root, s)
	if holder == nil {
		return errors.New("split not in tree")
	}
	if dropA {
		*holder = s.B
	} else {
		*holder = s.A
	}
	t.Focus = anyLeafID(*holder)
	return nil
}

// findSplitHolder returns a pointer to the *TreeNode slot whose Split ==
// target, or nil if not found.
func findSplitHolder(n *TreeNode, target *Split) *TreeNode {
	if n.IsLeaf() {
		return nil
	}
	if n.Split == target {
		return n
	}
	if h := findSplitHolder(&n.Split.A, target); h != nil {
		return h
	}
	return findSplitHolder(&n.Split.B, target)
}

// Swap exchanges the positions of two panes in the tree. After Swap,
// the pane previously at idA's tree position now sits where idB was,
// and vice versa. Per-pane state (selection, animation) keyed by pane
// id travels with the pane content automatically.
//
// idA == idB is a no-op (returns nil). Either id missing returns an
// error and leaves the tree unchanged.
//
// Focus is NOT moved by Swap; the caller decides where focus goes
// after the swap based on the input gesture (e.g., release pane).
func (t *Tree) Swap(idA, idB string) error {
	if idA == idB {
		return nil
	}
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

// SetRatio sets the ratio of the split that contains the named pane as one
// of its direct children. Returns false if no such split exists (the pane
// is at root).
func (t *Tree) SetRatio(paneID string, ratio float64) bool {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	return setRatio(&t.Root, paneID, ratio)
}

func setRatio(n *TreeNode, paneID string, ratio float64) bool {
	if n.IsLeaf() {
		return false
	}
	if (n.Split.A.IsLeaf() && n.Split.A.Pane.ID == paneID) ||
		(n.Split.B.IsLeaf() && n.Split.B.Pane.ID == paneID) {
		n.Split.Ratio = ratio
		return true
	}
	if setRatio(&n.Split.A, paneID, ratio) {
		return true
	}
	return setRatio(&n.Split.B, paneID, ratio)
}
