// The pane-layout codec: the persisted wire form of a pane tree, stored as a
// pane tile's content blob.
//
// It is a versioned DTO rather than json.Marshal(Tree), because the in-memory
// Tree carries unexported state and changes shape when the model does, while
// LayoutV1's bytes are forever: decoding a v1 blob must work in every future
// Gridwell, and the golden fixture in wire_test.go pins it.
//
// A leaf persists its whole place, root first and namespace crossings
// included, and no view: a grid's framing row and a content tile's row own
// that, so a restored leaf shows what they hold (Frame.ViewPending).
//
// Every id in the layout is stored in the owning node's namespace frame. The
// encoder strips the pane tile's transit-chain prefix through rel and the
// decoder prepends it through abs, so a pane tile mounted over ssh restores
// against the chain the reader used to reach it.
package pane

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/josephburnett/gridwell/api/panelayout"
	"github.com/josephburnett/gridwell/api/rpc"
)

// ChainPrefix is the transit chain through which a pane tile's owning node is
// reached: "" for a local tile, "<ssh>/" for one hop. It is the abs a
// DecodeLayout passes and the rel an EncodeLayout strips. Built from
// rpc.NamespaceOf, never a local split.
func ChainPrefix(tileID string) string {
	ns := rpc.NamespaceOf(rpc.NamespaceOf(tileID))
	if ns == "" {
		return ""
	}
	return ns + "/"
}

// The persisted format, and the server's one reading of it, are
// api/panelayout. This file is the client half, Tree to LayoutV1 and back, and
// derives nothing the server also derives, so there is no second decoder to
// drift.
const LayoutMediaType = panelayout.LayoutMediaType

const layoutVersion = panelayout.Version

var ErrLayoutVersion = panelayout.ErrLayoutVersion

// Wire DTO aliases; the one format definition is api/panelayout's.
type (
	LayoutV1    = panelayout.LayoutV1
	LayoutNode  = panelayout.LayoutNode
	LayoutSplit = panelayout.LayoutSplit
	LayoutPane  = panelayout.LayoutPane
	LayoutFrame = panelayout.LayoutFrame
)

// EncodeLayout serializes the tree as a LayoutV1 blob. rel maps a client-view
// id into the owning node's frame, answering false for a leaf looking outside
// that node's reach; such a leaf serializes as home and its pane id comes back
// in skipped, so the caller can surface one notice. A nil rel is the identity.
// Encoding never mutates the tree and identical trees produce identical bytes,
// which is what lets the persister hash-diff and never write for a pure visit.
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
	// Pane ids are stored bare: the blob is the durable fact and the level
	// namespace is session presentation.
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

// encodeLeaf maps one pane's place into the owning node's frame; false means
// an id was outside it and the leaf was serialized as home.
//
// The place is written twice by design. Anchor, Path and TextFocus are the
// projection onto the innermost namespace level, which is all an older
// Gridwell can read and all panelayout.TextFocusIDs scans, so TextFocus is
// written whenever the leaf is in content, Place or no Place. Place, the whole
// frame stack, is omitted wherever ProjectionHolds, so the common blob stays
// byte-identical to what earlier versions wrote.
func encodeLeaf(p *Pane, rel func(string) (string, bool), idPrefix string) (*LayoutPane, bool) {
	bareID := strings.TrimPrefix(p.ID, idPrefix)
	home := &LayoutPane{ID: bareID}
	var place []LayoutFrame
	if !p.ProjectionHolds() {
		frames := p.Frames()
		place = make([]LayoutFrame, len(frames))
		for i, f := range frames {
			lf := LayoutFrame{Content: f.Content}
			if f.GridID != "" {
				g, ok := rel(f.GridID)
				if !ok {
					return home, false
				}
				lf.GridID = g
			}
			if f.Door != "" {
				d, ok := rel(f.Door)
				if !ok {
					return home, false
				}
				lf.Door = d
			}
			place[i] = lf
		}
	}
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
		ID: bareID, Anchor: anchor, Path: path, TextFocus: textFocus, Place: place,
	}, true
}

// DecodeLayout parses a layout blob back into a Tree. abs prepends the
// reader's transit-chain prefix onto every id, nil being the identity. A newer
// Gridwell's blob fails with a wrapped ErrLayoutVersion. Decoding is strict on
// structure, which Gridwell wrote, and loose on arrangement state: an unknown
// Focus falls back to the first leaf, an unknown Zoomed clears, ratios clamp.
// idPrefix namespaces the decoded pane ids, so stacked live trees cannot
// collide in the pane-keyed maps.
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

	// Ids must be present and unique, since client locals are keyed by them,
	// and nextID must clear the highest p<N> so later mints cannot collide.
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
	p := &Pane{ID: idPrefix + lp.ID, Stack: decodePlace(lp, abs)}
	p.Zoom = 1
	p.ViewPending = true
	return p
}

// decodePlace rebuilds a leaf's frame stack from Place, or from the
// Anchor/Path/TextFocus projection, which is all older blobs carry and what is
// written whenever it holds the whole place.
func decodePlace(lp *LayoutPane, abs func(string) string) Stack {
	if len(lp.Place) > 0 {
		frames := make([]Frame, len(lp.Place))
		for i, lf := range lp.Place {
			f := Frame{Content: lf.Content}
			if lf.GridID != "" {
				f.GridID = abs(lf.GridID)
			}
			if lf.Door != "" {
				f.Door = abs(lf.Door)
			}
			frames[i] = f
		}
		return StackOf(frames)
	}
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
	return StackAt(anchor, path, textFocus)
}

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
