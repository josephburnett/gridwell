// The pane-layout codec: the persisted wire form of a pane tree, stored as
// the content blob of a "pane" tile (a durable workspace).
//
// This is deliberately a versioned DTO, not json.Marshal(Tree): the in-memory
// Tree carries unexported state (nextID), untagged fields (Cx/Cy/Zoom), and
// legacy json tags (file_scroll_x) that must not become a frozen wire format.
// LayoutV1's bytes are forever — decoding a v1 blob must work in every future
// version of Gridwell (the tablesV1 philosophy; the golden fixture in
// wire_test.go pins it).
//
// What is NOT in the layout, by design (issue #13 + URL-place semantics): the
// Up portal frames, the in-namespace ascent stack, selection, caret, and
// native handles. A leaf persists a *place* — anchor + path + viewport —
// exactly the URL vocabulary; the return stacks stay session-scoped.
//
// Id relativity: every id in the layout (anchor, path segments, TextFocus) is
// stored in the OWNING NODE's namespace frame. The encoder strips the pane
// tile's own transit-chain prefix via rel; the decoder prepends it via abs —
// the same relativity rule URL path segments follow, so a workspace mounted
// over ssh restores against the chain the reader used to reach it.
package pane

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// LayoutMediaType tags the layout blob in the store.
const LayoutMediaType = "application/vnd.gridwell.pane-layout+json"

// layoutVersion is the current wire version. Bump only with a new DTO type
// and a decoder that still accepts every older version.
const layoutVersion = 1

// ErrLayoutVersion reports a layout blob written by a newer Gridwell than
// this one. Callers must treat the workspace as read-only: never overwrite
// a newer format with a downgrade.
var ErrLayoutVersion = errors.New("pane layout: unsupported version")

// LayoutV1 is wire version 1 of a persisted pane tree.
type LayoutV1 struct {
	V      int        `json:"v"`
	Root   LayoutNode `json:"root"`
	Focus  string     `json:"focus,omitempty"`
	Zoomed string     `json:"zoomed,omitempty"`
}

// LayoutNode mirrors TreeNode: exactly one of Pane or Split is non-nil.
type LayoutNode struct {
	Pane  *LayoutPane  `json:"pane,omitempty"`
	Split *LayoutSplit `json:"split,omitempty"`
}

// LayoutSplit mirrors Split.
type LayoutSplit struct {
	Dir   string     `json:"dir"`
	Ratio float64    `json:"ratio"`
	A     LayoutNode `json:"a"`
	B     LayoutNode `json:"b"`
}

// LayoutPane is a leaf's persisted place: anchor + path + viewport, plus the
// text-descent state. All ids are in the owning node's namespace frame.
type LayoutPane struct {
	ID          string   `json:"id"`
	Anchor      string   `json:"anchor,omitempty"`
	Path        []string `json:"path,omitempty"`
	Cx          float64  `json:"cx,omitempty"`
	Cy          float64  `json:"cy,omitempty"`
	Zoom        float64  `json:"zoom,omitempty"`
	TextFocus   string   `json:"text_focus,omitempty"`
	TextMode    string   `json:"text_mode,omitempty"`
	TextScrollX float64  `json:"text_scroll_x,omitempty"`
	TextScrollY float64  `json:"text_scroll_y,omitempty"`
	TextZoom    float64  `json:"text_zoom,omitempty"`
}

// EncodeLayout serializes the tree as a LayoutV1 blob.
//
// rel maps a client-view qualified id (anchor, path segment, TextFocus) into
// the owning node's namespace frame, returning ok=false when the id cannot be
// expressed there (the leaf is looking outside the owning node's reach). Such
// a leaf is serialized as home — empty anchor, empty path, zoom 1 — and its
// pane id is returned in skipped so the caller can surface one notice. A nil
// rel is the identity (a locally-owned pane tile has no prefix to strip).
//
// Encoding never mutates the tree, and identical trees produce identical
// bytes — the persister hash-diffs the output, so a pure visit never writes.
func EncodeLayout(t *Tree, rel func(id string) (string, bool)) (data []byte, skipped []string, err error) {
	if t == nil {
		return nil, nil, errors.New("pane layout: nil tree")
	}
	if rel == nil {
		rel = func(id string) (string, bool) { return id, true }
	}
	root, skipped, err := encodeNode(t.Root, rel, nil)
	if err != nil {
		return nil, nil, err
	}
	l := LayoutV1{V: layoutVersion, Root: root, Focus: t.Focus, Zoomed: t.Zoomed}
	data, err = json.Marshal(l)
	if err != nil {
		return nil, nil, err
	}
	return data, skipped, nil
}

func encodeNode(n TreeNode, rel func(string) (string, bool), skipped []string) (LayoutNode, []string, error) {
	if n.IsLeaf() {
		lp, ok := encodeLeaf(n.Pane, rel)
		if !ok {
			skipped = append(skipped, n.Pane.ID)
		}
		return LayoutNode{Pane: lp}, skipped, nil
	}
	if n.Split == nil {
		return LayoutNode{}, skipped, errors.New("pane layout: node with neither pane nor split")
	}
	a, skipped, err := encodeNode(n.Split.A, rel, skipped)
	if err != nil {
		return LayoutNode{}, skipped, err
	}
	b, skipped, err := encodeNode(n.Split.B, rel, skipped)
	if err != nil {
		return LayoutNode{}, skipped, err
	}
	return LayoutNode{Split: &LayoutSplit{
		Dir: string(n.Split.Dir), Ratio: n.Split.Ratio, A: a, B: b,
	}}, skipped, nil
}

// encodeLeaf maps one pane's place into the owning node's frame. ok=false
// means some id was outside the frame and the leaf was serialized as home.
func encodeLeaf(p *Pane, rel func(string) (string, bool)) (*LayoutPane, bool) {
	home := &LayoutPane{ID: p.ID, Zoom: 1}
	anchor := ""
	if p.Anchor != "" {
		a, ok := rel(p.Anchor)
		if !ok {
			return home, false
		}
		anchor = a
	}
	var path []string
	if len(p.Path) > 0 {
		path = make([]string, len(p.Path))
		for i, seg := range p.Path {
			s, ok := rel(seg)
			if !ok {
				return home, false
			}
			path[i] = s
		}
	}
	textFocus := ""
	if p.TextFocus != "" {
		tf, ok := rel(p.TextFocus)
		if !ok {
			return home, false
		}
		textFocus = tf
	}
	return &LayoutPane{
		ID: p.ID, Anchor: anchor, Path: path,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom,
		TextFocus: textFocus, TextMode: p.TextMode,
		TextScrollX: p.TextScrollX, TextScrollY: p.TextScrollY, TextZoom: p.TextZoom,
	}, true
}

// DecodeLayout parses a layout blob back into a Tree.
//
// abs prepends the reader's transit-chain prefix onto every id (nil = the
// identity). A blob written by a newer Gridwell fails with ErrLayoutVersion
// (wrapped); structural corruption fails with a plain error. Decoding is
// strict on structure (we wrote it) and loose on view state: an unknown
// Focus falls back to the first leaf, an unknown Zoomed clears, a zero Zoom
// becomes 1, ratios clamp to [0,1].
func DecodeLayout(data []byte, abs func(id string) string) (*Tree, error) {
	var l LayoutV1
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("pane layout: %w", err)
	}
	if l.V != layoutVersion {
		return nil, fmt.Errorf("%w: v=%d", ErrLayoutVersion, l.V)
	}
	if abs == nil {
		abs = func(id string) string { return id }
	}
	root, err := decodeNode(l.Root, abs)
	if err != nil {
		return nil, err
	}
	t := &Tree{Root: root}

	// Leaf inventory: ids must be present and unique (client locals are keyed
	// by them), and nextID must clear the highest p<N> so later mints cannot
	// collide with a surviving pane.
	seen := map[string]bool{}
	maxN := 0
	var first string
	var walkErr error
	t.Walk(func(p *Pane) {
		if walkErr != nil {
			return
		}
		if p.ID == "" {
			walkErr = errors.New("pane layout: leaf with empty id")
			return
		}
		if seen[p.ID] {
			walkErr = fmt.Errorf("pane layout: duplicate pane id %q", p.ID)
			return
		}
		seen[p.ID] = true
		if first == "" {
			first = p.ID
		}
		if n, ok := paneIDNum(p.ID); ok && n > maxN {
			maxN = n
		}
	})
	if walkErr != nil {
		return nil, walkErr
	}
	t.nextID = maxN

	t.Focus = l.Focus
	if !seen[t.Focus] {
		t.Focus = first
	}
	if seen[l.Zoomed] {
		t.Zoomed = l.Zoomed
	}
	return t, nil
}

func decodeNode(n LayoutNode, abs func(string) string) (TreeNode, error) {
	switch {
	case n.Pane != nil && n.Split != nil:
		return TreeNode{}, errors.New("pane layout: node with both pane and split")
	case n.Pane != nil:
		return TreeNode{Pane: decodeLeaf(n.Pane, abs)}, nil
	case n.Split != nil:
		dir := Direction(n.Split.Dir)
		if dir != Horizontal && dir != Vertical {
			return TreeNode{}, fmt.Errorf("pane layout: invalid split dir %q", n.Split.Dir)
		}
		a, err := decodeNode(n.Split.A, abs)
		if err != nil {
			return TreeNode{}, err
		}
		b, err := decodeNode(n.Split.B, abs)
		if err != nil {
			return TreeNode{}, err
		}
		return TreeNode{Split: &Split{Dir: dir, Ratio: clamp01(n.Split.Ratio), A: a, B: b}}, nil
	default:
		return TreeNode{}, errors.New("pane layout: node with neither pane nor split")
	}
}

func decodeLeaf(lp *LayoutPane, abs func(string) string) *Pane {
	p := &Pane{
		ID: lp.ID,
		Cx: lp.Cx, Cy: lp.Cy, Zoom: lp.Zoom,
		TextMode:    lp.TextMode,
		TextScrollX: lp.TextScrollX, TextScrollY: lp.TextScrollY, TextZoom: lp.TextZoom,
	}
	if lp.Anchor != "" {
		p.Anchor = abs(lp.Anchor)
	}
	if len(lp.Path) > 0 {
		p.Path = make([]string, len(lp.Path))
		for i, seg := range lp.Path {
			p.Path[i] = abs(seg)
		}
	}
	if lp.TextFocus != "" {
		p.TextFocus = abs(lp.TextFocus)
	}
	if p.Zoom == 0 {
		p.Zoom = 1
	}
	return p
}

// paneIDNum parses the N out of a "p<N>" pane id.
func paneIDNum(id string) (int, bool) {
	rest, ok := strings.CutPrefix(id, "p")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// LeafTextFocusIDs returns the TextFocus tile id of every leaf that has one —
// the workspace's content descents, in tree order. The delete-time ephemeral
// reap (issue #174) reads the references this way without any client
// machinery; ids are in whatever frame the decoder's abs produced.
func LeafTextFocusIDs(t *Tree) []string {
	var out []string
	var walk func(n TreeNode)
	walk = func(n TreeNode) {
		if n.Pane != nil && n.Pane.TextFocus != "" {
			out = append(out, n.Pane.TextFocus)
		}
		if n.Split != nil {
			walk(n.Split.A)
			walk(n.Split.B)
		}
	}
	walk(t.Root)
	return out
}
