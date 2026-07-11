package pane

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// treesEqual compares two trees on everything the layout persists: structure,
// split dir/ratio, leaf places and text state, Focus, Zoomed. Up frames and
// nextID are deliberately excluded — Up is session-only by design, and nextID
// is covered by the mint-no-collision assertion instead.
func treesEqual(a, b *Tree) bool {
	return a.Focus == b.Focus && a.Zoomed == b.Zoomed && nodesEqual(a.Root, b.Root)
}

func nodesEqual(a, b TreeNode) bool {
	if a.IsLeaf() != b.IsLeaf() {
		return false
	}
	if a.IsLeaf() {
		pa, pb := a.Pane, b.Pane
		if len(pa.Path) != len(pb.Path) {
			return false
		}
		for i := range pa.Path {
			if pa.Path[i] != pb.Path[i] {
				return false
			}
		}
		return pa.ID == pb.ID && pa.Anchor == pb.Anchor &&
			pa.Cx == pb.Cx && pa.Cy == pb.Cy && pa.Zoom == pb.Zoom &&
			pa.TextFocus == pb.TextFocus && pa.TextMode == pb.TextMode &&
			pa.TextScrollX == pb.TextScrollX && pa.TextScrollY == pb.TextScrollY &&
			pa.TextZoom == pb.TextZoom
	}
	return a.Split.Dir == b.Split.Dir && a.Split.Ratio == b.Split.Ratio &&
		nodesEqual(a.Split.A, b.Split.A) && nodesEqual(a.Split.B, b.Split.B)
}

// TestLayoutGoldenV1 pins the v1 wire format: these exact bytes were written
// by the first shipping version of the codec and MUST decode identically
// forever (the tablesV1 philosophy). If this test breaks, the change is
// rewriting history in every user's pane tiles — do not "fix" the fixture.
func TestLayoutGoldenV1(t *testing.T) {
	golden := `{"v":1,"root":{"split":{"dir":"v","ratio":0.25,` +
		`"a":{"pane":{"id":"p1","anchor":"aaaa/1","path":["aaaa/7","aaaa/9"],"cx":3.5,"cy":-2,"zoom":1.5}},` +
		`"b":{"pane":{"id":"p3","anchor":"bbbb/1","cx":1,"cy":2,"zoom":1,` +
		`"text_focus":"bbbb/12","text_mode":"rendered","text_scroll_x":10,"text_scroll_y":80,"text_zoom":1.25}}}},` +
		`"focus":"p3","zoomed":"p3"}`

	got, err := DecodeLayout([]byte(golden), nil)
	if err != nil {
		t.Fatalf("golden v1 blob failed to decode: %v", err)
	}
	want := &Tree{
		Root: TreeNode{Split: &Split{
			Dir: Vertical, Ratio: 0.25,
			A: TreeNode{Pane: &Pane{ID: "p1", Anchor: "aaaa/1", Path: []string{"aaaa/7", "aaaa/9"}, Cx: 3.5, Cy: -2, Zoom: 1.5}},
			B: TreeNode{Pane: &Pane{ID: "p3", Anchor: "bbbb/1", Cx: 1, Cy: 2, Zoom: 1,
				TextFocus: "bbbb/12", TextMode: "rendered", TextScrollX: 10, TextScrollY: 80, TextZoom: 1.25}},
		}},
		Focus:  "p3",
		Zoomed: "p3",
	}
	if !treesEqual(got, want) {
		t.Fatalf("golden decode mismatch:\ngot  %+v\nwant %+v", got, want)
	}
	// nextID cleared the highest surviving p<N>: the next mint must not
	// collide with p3.
	got.Focus = "p1"
	np, err := got.Split(Horizontal)
	if err != nil {
		t.Fatalf("split after decode: %v", err)
	}
	if np.ID == "p1" || np.ID == "p3" {
		t.Fatalf("post-decode mint collided: %s", np.ID)
	}
}

// randomTree builds a tree through the real mutation API so the generator
// can never produce a shape the app itself cannot.
func randomTree(r *rand.Rand) *Tree {
	t := NewTree()
	splits := r.Intn(5)
	for range splits {
		leaves := collectIDs(t)
		if err := t.SetFocus(leaves[r.Intn(len(leaves))]); err != nil {
			panic(err)
		}
		side := Side(r.Intn(4))
		if _, err := t.SplitOnSideAt(side, 0.1+0.8*r.Float64()); err != nil {
			panic(err)
		}
	}
	i := 0
	t.Walk(func(p *Pane) {
		i++
		p.Anchor = fmt.Sprintf("uuid-%d/1", i%3)
		for range r.Intn(3) {
			p.Path = append(p.Path, fmt.Sprintf("uuid-%d/%d", i%3, r.Intn(90)+2))
		}
		p.Cx, p.Cy = float64(r.Intn(41)-20), float64(r.Intn(41)-20)
		p.Zoom = 0.25 * float64(r.Intn(8)+1)
		if r.Intn(3) == 0 {
			p.TextFocus = fmt.Sprintf("uuid-%d/%d", i%3, r.Intn(90)+2)
			p.TextMode = []string{"text", "rendered"}[r.Intn(2)]
			p.TextScrollX, p.TextScrollY = float64(r.Intn(200)), float64(r.Intn(200))
			p.TextZoom = 1 + r.Float64()
		}
	})
	leaves := collectIDs(t)
	if err := t.SetFocus(leaves[r.Intn(len(leaves))]); err != nil {
		panic(err)
	}
	if r.Intn(3) == 0 {
		t.ToggleZoom(leaves[r.Intn(len(leaves))])
	}
	return t
}

func collectIDs(t *Tree) []string {
	var ids []string
	t.Walk(func(p *Pane) { ids = append(ids, p.ID) })
	return ids
}

// TestLayoutRoundTripProperty: for arbitrary trees built through the real
// mutation API, Encode → Decode is the identity on everything persisted,
// encoding is deterministic (the persister hash-diffs bytes), and a mint
// after decode never collides with a surviving pane id.
func TestLayoutRoundTripProperty(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := range 200 {
		orig := randomTree(r)
		data, skipped, err := EncodeLayout(orig, nil)
		if err != nil {
			t.Fatalf("case %d: encode: %v", i, err)
		}
		if len(skipped) != 0 {
			t.Fatalf("case %d: identity rel skipped %v", i, skipped)
		}
		data2, _, err := EncodeLayout(orig, nil)
		if err != nil || !bytes.Equal(data, data2) {
			t.Fatalf("case %d: encoding is not deterministic", i)
		}
		got, err := DecodeLayout(data, nil)
		if err != nil {
			t.Fatalf("case %d: decode: %v", i, err)
		}
		if !treesEqual(got, orig) {
			t.Fatalf("case %d: round trip mismatch:\nblob %s\ngot  %+v\nwant %+v", i, data, got, orig)
		}
		before := collectIDs(got)
		np, err := got.Split(Horizontal)
		if err != nil {
			t.Fatalf("case %d: split after decode: %v", i, err)
		}
		for _, id := range before {
			if np.ID == id {
				t.Fatalf("case %d: post-decode mint %q collides", i, np.ID)
			}
		}
	}
}

// TestLayoutPrefixRelativity: ids are stored in the owning node's frame. The
// encoder strips the reader's transit prefix, the decoder prepends it — and a
// reader on a DIFFERENT chain (a second hop) restores the same blob against
// its own prefix.
func TestLayoutPrefixRelativity(t *testing.T) {
	const prefix = "ssh-a/ssh-b/" // two transit hops
	rel := func(id string) (string, bool) {
		rest, ok := strings.CutPrefix(id, prefix)
		return rest, ok
	}
	abs := func(id string) string { return prefix + id }

	tr := NewTree()
	p := tr.FocusedPane()
	p.Anchor = prefix + "plugin-uuid/1"
	p.Path = []string{prefix + "plugin-uuid/4", prefix + "plugin-uuid/9"}
	p.TextFocus = prefix + "plugin-uuid/12"
	p.Zoom = 2

	data, skipped, err := EncodeLayout(tr, rel)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("encode: err=%v skipped=%v", err, skipped)
	}
	// The blob itself must carry owner-frame ids (no reader prefix baked in).
	if bytes.Contains(data, []byte("ssh-a")) {
		t.Fatalf("blob leaked the reader's transit prefix: %s", data)
	}

	got, err := DecodeLayout(data, abs)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	gp := got.FocusedPane()
	if gp.Anchor != p.Anchor || gp.Path[0] != p.Path[0] || gp.Path[1] != p.Path[1] || gp.TextFocus != p.TextFocus {
		t.Fatalf("prefix round trip mismatch: %+v", gp)
	}

	// A different reader chain resolves the same blob against ITS prefix.
	other, err := DecodeLayout(data, func(id string) string { return "tunnel-z/" + id })
	if err != nil {
		t.Fatalf("decode via other chain: %v", err)
	}
	if op := other.FocusedPane(); op.Anchor != "tunnel-z/plugin-uuid/1" {
		t.Fatalf("other-chain anchor: %q", op.Anchor)
	}
}

// TestLayoutLeafOutsidePrefix: a leaf looking outside the owning node's reach
// cannot be expressed in the blob — it serializes as home and is reported in
// skipped, and the rest of the layout is unaffected.
func TestLayoutLeafOutsidePrefix(t *testing.T) {
	const prefix = "ssh-a/"
	rel := func(id string) (string, bool) {
		rest, ok := strings.CutPrefix(id, prefix)
		return rest, ok
	}

	tr := NewTree()
	inside := tr.FocusedPane()
	inside.Anchor = prefix + "plugin/1"
	inside.Cx, inside.Cy, inside.Zoom = 5, 6, 2
	outside, err := tr.Split(Vertical)
	if err != nil {
		t.Fatal(err)
	}
	outside.Anchor = "local-plugin/1" // reachable by the reader, not by the owner
	outside.Path = []string{"local-plugin/3"}
	outside.Cx, outside.Zoom = 9, 3

	data, skipped, err := EncodeLayout(tr, rel)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(skipped) != 1 || skipped[0] != outside.ID {
		t.Fatalf("skipped = %v, want [%s]", skipped, outside.ID)
	}
	got, err := DecodeLayout(data, func(id string) string { return prefix + id })
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	gi, go_ := got.FindPane(inside.ID), got.FindPane(outside.ID)
	if gi.Anchor != inside.Anchor || gi.Cx != 5 {
		t.Fatalf("inside leaf damaged: %+v", gi)
	}
	if go_.Anchor != "" || len(go_.Path) != 0 || go_.Zoom != 1 {
		t.Fatalf("outside leaf should be home: %+v", go_)
	}
}

func TestLayoutUnknownVersion(t *testing.T) {
	_, err := DecodeLayout([]byte(`{"v":2,"root":{"pane":{"id":"p1"}}}`), nil)
	if !errors.Is(err, ErrLayoutVersion) {
		t.Fatalf("want ErrLayoutVersion, got %v", err)
	}
	_, err = DecodeLayout([]byte(`{"root":{"pane":{"id":"p1"}}}`), nil) // v absent = 0
	if !errors.Is(err, ErrLayoutVersion) {
		t.Fatalf("want ErrLayoutVersion for missing v, got %v", err)
	}
}

func TestLayoutMalformed(t *testing.T) {
	cases := map[string]string{
		"not json":       `{"v":1,`,
		"empty node":     `{"v":1,"root":{}}`,
		"both node":      `{"v":1,"root":{"pane":{"id":"p1"},"split":{"dir":"h","ratio":0.5,"a":{"pane":{"id":"p2"}},"b":{"pane":{"id":"p3"}}}}}`,
		"bad dir":        `{"v":1,"root":{"split":{"dir":"x","ratio":0.5,"a":{"pane":{"id":"p1"}},"b":{"pane":{"id":"p2"}}}}}`,
		"empty leaf id":  `{"v":1,"root":{"pane":{"id":""}}}`,
		"duplicate leaf": `{"v":1,"root":{"split":{"dir":"h","ratio":0.5,"a":{"pane":{"id":"p1"}},"b":{"pane":{"id":"p1"}}}}}`,
	}
	for name, blob := range cases {
		if _, err := DecodeLayout([]byte(blob), nil); err == nil {
			t.Errorf("%s: decode accepted malformed blob", name)
		}
	}
}

// TestLayoutLooseViewState: view-state fields are restored loosely — an
// unknown Focus falls back to the first leaf, an unknown Zoomed clears, a
// zero Zoom becomes 1, and an out-of-range ratio clamps.
func TestLayoutLooseViewState(t *testing.T) {
	blob := `{"v":1,"root":{"split":{"dir":"h","ratio":1.7,` +
		`"a":{"pane":{"id":"p1"}},"b":{"pane":{"id":"p2","zoom":2}}}},` +
		`"focus":"p99","zoomed":"p98"}`
	got, err := DecodeLayout([]byte(blob), nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Focus != "p1" {
		t.Errorf("focus fallback: got %q, want p1", got.Focus)
	}
	if got.Zoomed != "" {
		t.Errorf("unknown zoomed should clear, got %q", got.Zoomed)
	}
	if p := got.FindPane("p1"); p.Zoom != 1 {
		t.Errorf("zero zoom should default to 1, got %v", p.Zoom)
	}
	if got.Root.Split.Ratio != 1 {
		t.Errorf("ratio should clamp to 1, got %v", got.Root.Split.Ratio)
	}
}

// TestLayoutDropsUpFrames: the portal ascent stack is session-only by design
// (issue #13); it must never reach the blob.
func TestLayoutDropsUpFrames(t *testing.T) {
	tr := NewTree()
	p := tr.FocusedPane()
	p.Anchor = "plugin/1"
	p.PushFrame(true)
	p.Anchor = "other/1"

	data, _, err := EncodeLayout(tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"up"`)) || bytes.Contains(data, []byte("plugin/1")) {
		t.Fatalf("Up frames leaked into the blob: %s", data)
	}
	got, err := DecodeLayout(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FocusedPane().Up) != 0 {
		t.Fatal("decoded pane has Up frames")
	}
}

// TestLeafTextFocusIDs: the delete-time ephemeral reap (issue #174) reads a
// workspace's content descents straight off the decoded tree — every leaf's
// TextFocus, in tree order, empty ones skipped.
func TestLeafTextFocusIDs(t *testing.T) {
	blob := []byte(`{"v":1,"root":{"split":{"dir":"v","ratio":0.5,` +
		`"a":{"pane":{"id":"p1","anchor":"u/1","cx":0.5,"cy":0.5,"zoom":1,"text_focus":"u/7"}},` +
		`"b":{"split":{"dir":"h","ratio":0.5,` +
		`"a":{"pane":{"id":"p2","anchor":"u/1","cx":0.5,"cy":0.5,"zoom":1}},` +
		`"b":{"pane":{"id":"p3","anchor":"u/1","cx":0.5,"cy":0.5,"zoom":1,"text_focus":"u/9"}}}}}},"focus":"p1"}`)
	tree, err := DecodeLayout(blob, func(id string) string { return id })
	if err != nil {
		t.Fatalf("DecodeLayout: %v", err)
	}
	got := LeafTextFocusIDs(tree)
	if len(got) != 2 || got[0] != "u/7" || got[1] != "u/9" {
		t.Errorf("LeafTextFocusIDs = %v, want [u/7 u/9]", got)
	}
}
