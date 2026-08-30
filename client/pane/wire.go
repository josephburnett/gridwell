// The pane-layout codec: the persisted wire form of a pane tree, stored as
// the content blob of a "pane" tile (a durable workspace).
//
// This is deliberately a versioned DTO, not json.Marshal(Tree): the in-memory
// Tree carries unexported state (nextID) and untagged fields, and it changes
// shape when the model does (the frame stack replaced five representations of
// a pane's place in 2026-08-29's S8) — neither may become a frozen wire
// format. LayoutV1's bytes are forever — decoding a v1 blob must work in every future
// version of Gridwell (the tablesV1 philosophy; the golden fixture in
// wire_test.go pins it).
//
// What is NOT in the layout, by design (issue #13 + URL-place semantics):
// the OUTER frames' viewports, the selection, and the native handles. A leaf
// persists the place it is AT — the grid it sits in, the doorways it came
// through, and the viewport there — exactly the URL vocabulary; the
// viewports it would ascend onto stay session-scoped, so a restored pane
// falls back to each grid's persisted framing on the way out.
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
	"github.com/josephburnett/gridwell/api/panelayout"
	"strconv"
	"strings"
)

// The persisted format itself — structs, media type, version — is
// api/panelayout (CONTRACT: the localdb store reads the same blobs for
// its reap's protection set; one definition, no second decoder to
// drift). This file is the CLIENT half: Tree <-> LayoutV1 conversion.
const LayoutMediaType = panelayout.LayoutMediaType

const layoutVersion = panelayout.Version

// ErrLayoutVersion re-exports panelayout.ErrLayoutVersion.
var ErrLayoutVersion = panelayout.ErrLayoutVersion

// Wire DTO aliases — the one format definition lives in api/panelayout.
type (
	LayoutV1    = panelayout.LayoutV1
	LayoutNode  = panelayout.LayoutNode
	LayoutSplit = panelayout.LayoutSplit
	LayoutPane  = panelayout.LayoutPane
)

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
	root, skipped, err := encodeNode(t.Root, rel, t.IDPrefix, nil)
	if err != nil {
		return nil, nil, err
	}
	// Pane ids are stored BARE (t.IDPrefix stripped): the blob is the
	// durable fact; the level namespace is session presentation (#249).
	l := LayoutV1{V: layoutVersion, Root: root,
		Focus:  strings.TrimPrefix(t.Focus, t.IDPrefix),
		Zoomed: strings.TrimPrefix(t.Zoomed, t.IDPrefix)}
	data, err = json.Marshal(l)
	if err != nil {
		return nil, nil, err
	}
	return data, skipped, nil
}

func encodeNode(n TreeNode, rel func(string) (string, bool), idPrefix string, skipped []string) (LayoutNode, []string, error) {
	if n.IsLeaf() {
		lp, ok := encodeLeaf(n.Pane, rel, idPrefix)
		if !ok {
			skipped = append(skipped, n.Pane.ID)
		}
		return LayoutNode{Pane: lp}, skipped, nil
	}
	if n.Split == nil {
		return LayoutNode{}, skipped, errors.New("pane layout: node with neither pane nor split")
	}
	a, skipped, err := encodeNode(n.Split.A, rel, idPrefix, skipped)
	if err != nil {
		return LayoutNode{}, skipped, err
	}
	b, skipped, err := encodeNode(n.Split.B, rel, idPrefix, skipped)
	if err != nil {
		return LayoutNode{}, skipped, err
	}
	return LayoutNode{Split: &LayoutSplit{
		Dir: string(n.Split.Dir), Ratio: n.Split.Ratio, A: a, B: b,
	}}, skipped, nil
}

// encodeLeaf maps one pane's place into the owning node's frame. ok=false
// means some id was outside the frame and the leaf was serialized as home.
func encodeLeaf(p *Pane, rel func(string) (string, bool), idPrefix string) (*LayoutPane, bool) {
	bareID := strings.TrimPrefix(p.ID, idPrefix)
	home := &LayoutPane{ID: bareID, Zoom: 1}
	panchor, ppath := p.AnchorPathAt(p.Depth() - 1)
	anchor := ""
	if panchor != "" {
		a, ok := rel(panchor)
		if !ok {
			return home, false
		}
		anchor = a
	}
	var path []string
	if len(ppath) > 0 {
		path = make([]string, len(ppath))
		for i, seg := range ppath {
			s, ok := rel(seg)
			if !ok {
				return home, false
			}
			path[i] = s
		}
	}
	textFocus := ""
	if id := p.ContentID(); id != "" {
		tf, ok := rel(id)
		if !ok {
			return home, false
		}
		textFocus = tf
	}
	return &LayoutPane{
		ID: bareID, Anchor: anchor, Path: path,
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
// idPrefix namespaces the decoded panes' ids (issue #249): the blob's
// bare "p<N>" ids become "<idPrefix>p<N>", and the tree mints with the
// same prefix, so stacked live trees can never collide in the pane-keyed
// maps (locals, native views, shell streams). "" decodes verbatim.
func DecodeLayout(data []byte, abs func(id string) string, idPrefix string) (*Tree, error) {
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
	root, err := decodeNode(l.Root, abs, idPrefix)
	if err != nil {
		return nil, err
	}
	t := &Tree{Root: root, IDPrefix: idPrefix}

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
		if n, ok := paneIDNum(strings.TrimPrefix(p.ID, idPrefix)); ok && n > maxN {
			maxN = n
		}
	})
	if walkErr != nil {
		return nil, walkErr
	}
	t.nextID = maxN

	t.Focus = idPrefix + l.Focus
	if !seen[t.Focus] {
		t.Focus = first
	}
	if l.Zoomed != "" && seen[idPrefix+l.Zoomed] {
		t.Zoomed = idPrefix + l.Zoomed
	}
	return t, nil
}

func decodeNode(n LayoutNode, abs func(string) string, idPrefix string) (TreeNode, error) {
	switch {
	case n.Pane != nil && n.Split != nil:
		return TreeNode{}, errors.New("pane layout: node with both pane and split")
	case n.Pane != nil:
		return TreeNode{Pane: decodeLeaf(n.Pane, abs, idPrefix)}, nil
	case n.Split != nil:
		dir := Direction(n.Split.Dir)
		if dir != Horizontal && dir != Vertical {
			return TreeNode{}, fmt.Errorf("pane layout: invalid split dir %q", n.Split.Dir)
		}
		a, err := decodeNode(n.Split.A, abs, idPrefix)
		if err != nil {
			return TreeNode{}, err
		}
		b, err := decodeNode(n.Split.B, abs, idPrefix)
		if err != nil {
			return TreeNode{}, err
		}
		return TreeNode{Split: &Split{Dir: dir, Ratio: clamp01(n.Split.Ratio), A: a, B: b}}, nil
	default:
		return TreeNode{}, errors.New("pane layout: node with neither pane nor split")
	}
}

func decodeLeaf(lp *LayoutPane, abs func(string) string, idPrefix string) *Pane {
	anchor := ""
	if lp.Anchor != "" {
		anchor = abs(lp.Anchor)
	}
	path := make([]string, len(lp.Path))
	for i, seg := range lp.Path {
		path[i] = abs(seg)
	}
	textFocus := ""
	if lp.TextFocus != "" {
		textFocus = abs(lp.TextFocus)
	}
	p := &Pane{ID: idPrefix + lp.ID, Stack: StackAt(anchor, path, textFocus)}
	p.Cx, p.Cy, p.Zoom = lp.Cx, lp.Cy, lp.Zoom
	p.TextMode = lp.TextMode
	p.TextScrollX, p.TextScrollY, p.TextZoom = lp.TextScrollX, lp.TextScrollY, lp.TextZoom
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
		if n.Pane != nil {
			if id := n.Pane.ContentID(); id != "" {
				out = append(out, id)
			}
		}
		if n.Split != nil {
			walk(n.Split.A)
			walk(n.Split.B)
		}
	}
	walk(t.Root)
	return out
}
