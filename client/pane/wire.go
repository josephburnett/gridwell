// The pane-layout codec: the persisted wire form of a pane tree, stored as
// the content blob of a "pane" tile (a durable workspace).
//
// This is deliberately a versioned DTO, not json.Marshal(Tree): the in-memory
// Tree carries unexported state (nextID) and untagged fields, and it changes
// shape when the model does, so neither may become a frozen wire format.
// LayoutV1's bytes are forever — decoding a v1 blob must work in every future
// version of Gridwell, and the golden fixture in wire_test.go pins it.
//
// A leaf persists its whole place: every frame it descended through, root
// first, namespace crossings included, plus the viewport at the one it is on.
// The bar's chain is that stack projected, so a restored workspace can always
// be walked back out to its root.
//
// What is not in the layout, by design: the outer frames' viewports, the
// selection, and the native handles. The viewports a pane would ascend onto
// stay session-scoped, so a restored pane falls back to each grid's persisted
// framing on the way out.
//
// Id relativity: every id in the layout (anchor, path segments, TextFocus,
// and each Place frame's door and grid) is stored in the owning node's
// namespace frame. The encoder strips the pane tile's own transit-chain
// prefix via rel; the decoder prepends it via abs. URL path segments follow
// the same rule, so a pane tile mounted over ssh restores against the chain
// the reader used to reach it.
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

// ChainPrefix returns the transit-chain prefix through which a pane tile's
// owning node is reached — everything before the owning-plugin segment ("" for
// a local tile, "<ssh>/" for one hop, and so on). The blob's ids are stored in
// the owning node's frame, per the relativity rule above, and the reader
// prepends this prefix to resolve them in its own view: it is the `abs` half a
// DecodeLayout call passes and the `rel` half an EncodeLayout call strips.
// Built from the shared id codec (rpc.NamespaceOf), never a local split.
func ChainPrefix(tileID string) string {
	ns := rpc.NamespaceOf(rpc.NamespaceOf(tileID))
	if ns == "" {
		return ""
	}
	return ns + "/"
}

// The persisted format itself — structs, media type, version — is
// api/panelayout, and so is the server's one reading of it: which content
// tiles a blob references (panelayout.TextFocusIDs) is answered there for both
// the boot sweep's protection set and the delete-time reap. This file is the
// client half — Tree to LayoutV1 and back — and it derives nothing the server
// also derives, so there is no second decoder to drift.
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
	LayoutFrame = panelayout.LayoutFrame
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
	// Pane ids are stored bare, with t.IDPrefix stripped: the blob is the
	// durable fact and the level namespace is session presentation.
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
//
// The place is written twice by design: Place is the whole frame stack, and
// Anchor/Path/TextFocus are its projection onto the innermost namespace
// level, which is all a Gridwell older than Place can read and all
// panelayout.TextFocusIDs scans. TextFocus is therefore written whenever the
// leaf is descended into content, Place or no Place: it is the server's one
// record of the reference. Place is omitted wherever the projection already
// holds the whole place (Stack.ProjectionHolds), so the common blob is
// byte-identical to what earlier versions wrote and a pure visit still never
// writes.
func encodeLeaf(p *Pane, rel func(string) (string, bool), idPrefix string) (*LayoutPane, bool) {
	bareID := strings.TrimPrefix(p.ID, idPrefix)
	home := &LayoutPane{ID: bareID, Zoom: 1}
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
		ID: bareID, Anchor: anchor, Path: path,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom,
		TextFocus: textFocus, TextMode: p.TextMode,
		TextScrollX: p.TextScrollX, TextScrollY: p.TextScrollY, TextZoom: p.TextZoom,
		Place: place,
	}, true
}

// DecodeLayout parses a layout blob back into a Tree.
//
// abs prepends the reader's transit-chain prefix onto every id (nil = the
// identity). A blob written by a newer Gridwell fails with ErrLayoutVersion
// (wrapped); structural corruption fails with a plain error. Decoding is
// strict on structure, which Gridwell wrote, and loose on view state: an
// unknown Focus falls back to the first leaf, an unknown Zoomed clears, a
// zero Zoom becomes 1, and ratios clamp to [0,1].
//
// idPrefix namespaces the decoded panes' ids: the blob's bare "p<N>" ids
// become "<idPrefix>p<N>", and the tree mints with the same prefix, so
// stacked live trees cannot collide in the pane-keyed maps (locals, native
// views, shell streams). "" decodes verbatim.
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
	p := &Pane{ID: idPrefix + lp.ID, Stack: decodePlace(lp, abs)}
	p.Cx, p.Cy, p.Zoom = lp.Cx, lp.Cy, lp.Zoom
	p.TextMode = lp.TextMode
	p.TextScrollX, p.TextScrollY, p.TextZoom = lp.TextScrollX, lp.TextScrollY, lp.TextZoom
	if p.Zoom == 0 {
		p.Zoom = 1
	}
	return p
}

// decodePlace rebuilds a leaf's frame stack: Place when the blob recorded it
// level by level, and otherwise its Anchor/Path/TextFocus projection — the
// only shape blobs written before Place carry, and the shape written whenever
// it holds the whole place.
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
