// Package pane implements the tmux-style pane tree used by the Ascent client.
//
// All logic here is pure Go: no syscall/js, no network. The package is
// imported by the WASM entry point and is fully covered by standard
// `go test`.
package pane

import (
	"errors"
	"fmt"
)

// Direction identifies a split orientation. "h" is a horizontal divider
// (top/bottom panes); "v" is vertical (left/right). Matches the spec §10.4
// JSON wire format.
type Direction string

const (
	Horizontal Direction = "h"
	Vertical   Direction = "v"
)

// Pane is a leaf in the pane tree: one viewport. Path is the descent path
// (well row ids) from root to the currently-viewed grid. Cx, Cy are the
// viewport center in cells; Zoom is the pane's zoom multiplier.
//
// FileFocus, when nonzero, marks the pane as "descended into" a file node:
// the pane still sits in the parent grid (Path is unchanged), but the
// chrome and input semantics switch to file-editing mode. FileMode picks
// between "text" (raw markdown in a textarea overlay) and "rendered" (the
// markdown layout rendered into the canvas). FileScrollY is the vertical
// scroll inside the file's interior in logical pixels; mirrored to the
// file node's view_y on save.
type Pane struct {
	ID   string
	Path []int64
	Cx   float64
	Cy   float64
	Zoom float64

	FileFocus   int64   `json:"file_focus,omitempty"`
	FileMode    string  `json:"file_mode,omitempty"`
	FileScrollY float64 `json:"file_scroll_y,omitempty"`
}

// Clone returns a deep copy of the pane (including the path slice).
func (p *Pane) Clone(newID string) *Pane {
	c := *p
	c.ID = newID
	if len(p.Path) > 0 {
		c.Path = append([]int64(nil), p.Path...)
	}
	return &c
}

// Split is an internal node in the pane tree. Ratio is in [0, 1]; A is the
// top/left child, B is the bottom/right.
type Split struct {
	Dir   Direction
	Ratio float64
	A     Node
	B     Node
}

// Node is the sum type of pane-tree nodes. Exactly one of *Pane or *Split
// is non-nil. We model it explicitly rather than as an interface to keep
// JSON marshaling straightforward.
type Node struct {
	Pane  *Pane
	Split *Split
}

// IsLeaf reports whether the node is a leaf pane.
func (n Node) IsLeaf() bool { return n.Pane != nil }

// Tree is the whole pane state, plus the id of the keyboard-focused pane.
type Tree struct {
	Root  Node
	Focus string
	// nextID is incremented on each split to mint fresh pane ids.
	nextID int
}

// NewTree returns a fresh tree with a single pane at root.
func NewTree() *Tree {
	t := &Tree{nextID: 1}
	pane := &Pane{ID: "p1", Zoom: 1.0}
	t.Root = Node{Pane: pane}
	t.Focus = pane.ID
	return t
}

// Walk visits every leaf pane in tree order and calls fn for each.
func (t *Tree) Walk(fn func(*Pane)) {
	walk(t.Root, fn)
}

func walk(n Node, fn func(*Pane)) {
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
	t.Root, replaced = replacePane(t.Root, focused.ID, Node{
		Split: &Split{
			Dir: dir, Ratio: 0.5,
			A: Node{Pane: focused},
			B: Node{Pane: newPane},
		},
	})
	if !replaced {
		return nil, errors.New("internal: focused pane not in tree")
	}
	return newPane, nil
}

// replacePane substitutes the leaf pane with id targetID for replacement.
// Returns the new node and whether a replacement occurred.
func replacePane(n Node, targetID string, replacement Node) (Node, bool) {
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
func dropPane(n Node, targetID string) (Node, string, bool) {
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

func anyLeafID(n Node) string {
	if n.IsLeaf() {
		return n.Pane.ID
	}
	if id := anyLeafID(n.Split.A); id != "" {
		return id
	}
	return anyLeafID(n.Split.B)
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

func setRatio(n *Node, paneID string, ratio float64) bool {
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

// TruncatePathTo returns prefix p truncated to the deepest still-valid prefix
// given the set of currently-known well row ids. If the entire path is
// invalid, returns nil (caller resets to root grid).
//
// "Valid" means the well id exists in known. The function does not validate
// that the wells form a coherent chain — that's the server's job — it only
// trims trailing ids that vanished.
func TruncatePathTo(p []int64, known map[int64]bool) []int64 {
	for i := len(p); i > 0; i-- {
		if known[p[i-1]] {
			return append([]int64(nil), p[:i]...)
		}
	}
	return nil
}
