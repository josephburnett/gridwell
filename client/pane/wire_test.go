package pane

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/panelayout"
)

// treesEqual compares two trees on everything the layout persists: structure,
// split dir/ratio, every leaf's whole place and text state, Focus, Zoomed. The
// outer frames' viewports and nextID are deliberately excluded — those
// viewports are session-only by design (wire.go), and nextID is covered by the
// mint-no-collision assertion instead.
func treesEqual(a, b *Tree) bool {
	return a.Focus == b.Focus && a.Zoomed == b.Zoomed && nodesEqual(a.Root, b.Root)
}

// placesEqual compares two panes' frame stacks level by level on the identity
// the blob carries: which grid a frame opens, which doorway it came through,
// and whether the door is the place. Outer viewports are excluded — see
// treesEqual.
func placesEqual(a, b *Pane) bool {
	fa, fb := a.Frames(), b.Frames()
	if len(fa) != len(fb) {
		return false
	}
	for i := range fa {
		if fa[i].GridID != fb[i].GridID || fa[i].Door != fb[i].Door ||
			fa[i].Content != fb[i].Content {
			return false
		}
	}
	return true
}

func nodesEqual(a, b TreeNode) bool {
	if a.IsLeaf() != b.IsLeaf() {
		return false
	}
	if a.IsLeaf() {
		pa, pb := a.Pane, b.Pane
		if !placesEqual(pa, pb) {
			return false
		}
		return pa.ID == pb.ID &&
			pa.Cx == pb.Cx && pa.Cy == pb.Cy && pa.Zoom == pb.Zoom &&
			pa.TextMode == pb.TextMode &&
			pa.TextScrollX == pb.TextScrollX && pa.TextScrollY == pb.TextScrollY &&
			pa.TextZoom == pb.TextZoom
	}
	return a.Split.Dir == b.Split.Dir && a.Split.Ratio == b.Split.Ratio &&
		nodesEqual(a.Split.A, b.Split.A) && nodesEqual(a.Split.B, b.Split.B)
}

// TestLayoutGoldenV1 pins the v1 wire format: these exact bytes were written
// by the first shipping version of the codec and decode identically forever.
// If this test breaks, the change is rewriting history in every user's pane
// tiles — do not "fix" the fixture.
func TestLayoutGoldenV1(t *testing.T) {
	golden := `{"v":1,"root":{"split":{"dir":"v","ratio":0.25,` +
		`"a":{"pane":{"id":"p1","anchor":"aaaa/1","path":["aaaa/7","aaaa/9"],"cx":3.5,"cy":-2,"zoom":1.5}},` +
		`"b":{"pane":{"id":"p3","anchor":"bbbb/1","cx":1,"cy":2,"zoom":1,` +
		`"text_focus":"bbbb/12","text_mode":"rendered","text_scroll_x":10,"text_scroll_y":80,"text_zoom":1.25}}}},` +
		`"focus":"p3","zoomed":"p3"}`

	got, err := DecodeLayout([]byte(golden), nil, "")
	if err != nil {
		t.Fatalf("golden v1 blob failed to decode: %v", err)
	}
	want := &Tree{
		Root: TreeNode{Split: &Split{
			Dir: Vertical, Ratio: 0.25,
			A: TreeNode{Pane: goldenLeaf("p1", "aaaa/1", []string{"aaaa/7", "aaaa/9"}, "", 3.5, -2, 1.5)},
			B: TreeNode{Pane: goldenTextLeaf("p3", "bbbb/1", "bbbb/12")},
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
		var path []string
		for range r.Intn(3) {
			path = append(path, fmt.Sprintf("uuid-%d/%d", i%3, r.Intn(90)+2))
		}
		content := ""
		text := r.Intn(3) == 0
		if text {
			content = fmt.Sprintf("uuid-%d/%d", i%3, r.Intn(90)+2)
		}
		p.Stack = StackAt(fmt.Sprintf("uuid-%d/1", i%3), path, content)
		// Some panes cross into another namespace on the way down — the shape
		// whose outer levels the anchor/path projection cannot hold. The
		// crossing goes under any content descent, which stays the top frame.
		if r.Intn(3) == 0 {
			var leaf Frame
			hadContent := p.Content
			if hadContent {
				leaf = p.Frame
				p.Pop()
			}
			p.Push(Frame{GridID: fmt.Sprintf("uuid-%d/1", (i+1)%3),
				Door: fmt.Sprintf("uuid-%d/%d", i%3, r.Intn(90)+2)})
			for range r.Intn(2) {
				p.Push(Frame{Door: fmt.Sprintf("uuid-%d/%d", (i+1)%3, r.Intn(90)+2)})
			}
			if hadContent {
				p.Push(leaf)
			}
		}
		p.Cx, p.Cy = float64(r.Intn(41)-20), float64(r.Intn(41)-20)
		p.Zoom = 0.25 * float64(r.Intn(8)+1)
		if text {
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
		got, err := DecodeLayout(data, nil, "")
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
	p.Stack = StackAt(prefix+"plugin-uuid/1",
		[]string{prefix + "plugin-uuid/4", prefix + "plugin-uuid/9"}, prefix+"plugin-uuid/12")
	p.Zoom = 2

	data, skipped, err := EncodeLayout(tr, rel)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("encode: err=%v skipped=%v", err, skipped)
	}
	// The blob itself must carry owner-frame ids (no reader prefix baked in).
	if bytes.Contains(data, []byte("ssh-a")) {
		t.Fatalf("blob leaked the reader's transit prefix: %s", data)
	}

	got, err := DecodeLayout(data, abs, "")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	gp := got.FocusedPane()
	if gp.Anchor() != p.Anchor() || gp.Path()[0] != p.Path()[0] || gp.Path()[1] != p.Path()[1] || gp.ContentID() != p.ContentID() {
		t.Fatalf("prefix round trip mismatch: %+v", gp)
	}

	// A different reader chain resolves the same blob against ITS prefix.
	other, err := DecodeLayout(data, func(id string) string { return "tunnel-z/" + id }, "")
	if err != nil {
		t.Fatalf("decode via other chain: %v", err)
	}
	if op := other.FocusedPane(); op.Anchor() != "tunnel-z/plugin-uuid/1" {
		t.Fatalf("other-chain anchor: %q", op.Anchor())
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
	inside.Stack = StackAt(prefix+"plugin/1", nil, "")
	inside.Cx, inside.Cy, inside.Zoom = 5, 6, 2
	outside, err := tr.Split(Vertical)
	if err != nil {
		t.Fatal(err)
	}
	// reachable by the reader, not by the owner
	outside.Stack = StackAt("local-plugin/1", []string{"local-plugin/3"}, "")
	outside.Cx, outside.Zoom = 9, 3

	data, skipped, err := EncodeLayout(tr, rel)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(skipped) != 1 || skipped[0] != outside.ID {
		t.Fatalf("skipped = %v, want [%s]", skipped, outside.ID)
	}
	got, err := DecodeLayout(data, func(id string) string { return prefix + id }, "")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	gi, go_ := got.FindPane(inside.ID), got.FindPane(outside.ID)
	if gi.Anchor() != inside.Anchor() || gi.Cx != 5 {
		t.Fatalf("inside leaf damaged: %+v", gi)
	}
	if go_.Anchor() != "" || len(go_.Path()) != 0 || go_.Zoom != 1 {
		t.Fatalf("outside leaf should be home: %+v", go_)
	}
}

func TestLayoutUnknownVersion(t *testing.T) {
	_, err := DecodeLayout([]byte(`{"v":2,"root":{"pane":{"id":"p1"}}}`), nil, "")
	if !errors.Is(err, ErrLayoutVersion) {
		t.Fatalf("want ErrLayoutVersion, got %v", err)
	}
	_, err = DecodeLayout([]byte(`{"root":{"pane":{"id":"p1"}}}`), nil, "") // v absent = 0
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
		if _, err := DecodeLayout([]byte(blob), nil, ""); err == nil {
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
	got, err := DecodeLayout([]byte(blob), nil, "")
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

// TestLayoutKeepsEveryLevelFromTheRoot: a restored place carries every level
// down from the root, namespace crossings included. A crossing frame owns its
// target grid id, so the blob must carry it: without it the restored stack
// begins at the crossing, and the bar's chain has no root crumb and no way
// back out of the plugin.
func TestLayoutKeepsEveryLevelFromTheRoot(t *testing.T) {
	tr := NewTree()
	p := tr.FocusedPane()
	p.Stack = StackAt("plugin/1", []string{"plugin/4"}, "")
	p.Push(Frame{GridID: "other/1", Door: "plugin/9", Zoom: 1})

	data, _, err := EncodeLayout(tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeLayout(data, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	gp := got.FocusedPane()
	if gp.Depth() != 3 {
		t.Fatalf("restored depth = %d, want 3: %+v", gp.Depth(), gp.Crumbs())
	}
	// The leaf still names its own namespace directly: the crossing frame kept
	// its grid id, so nothing has to walk the parent grid to find it.
	if gp.Anchor() != "other/1" || len(gp.Path()) != 0 {
		t.Fatalf("restored leaf place = %q %v, want other/1 []", gp.Anchor(), gp.Path())
	}
	crumbs := gp.Crumbs()
	if len(crumbs) != 3 || crumbs[0].Anchor != "plugin/1" ||
		crumbs[1].TileID != "plugin/4" || crumbs[2].Anchor != "other/1" {
		t.Fatalf("restored chain = %+v", crumbs)
	}
}

// TestLayoutRestoresTheWholePlace: the crumb chain of a mixed deep place —
// wells, two crossings, a content descent below the top — survives the blob
// unchanged. The chain is the projection users navigate by, so it is what the
// round trip is measured on.
func TestLayoutRestoresTheWholePlace(t *testing.T) {
	tr := NewTree()
	src := chainPane()
	tr.FocusedPane().Stack = src.Stack.Clone()

	data, _, err := EncodeLayout(tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeLayout(data, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	want, gotCrumbs := src.Crumbs(), got.FocusedPane().Crumbs()
	if len(want) != len(gotCrumbs) {
		t.Fatalf("chain length = %d, want %d: %+v", len(gotCrumbs), len(want), gotCrumbs)
	}
	for i := range want {
		g, w := gotCrumbs[i], want[i]
		if g.Level != w.Level || g.Anchor != w.Anchor || g.TileID != w.TileID ||
			g.Text != w.Text || g.ParentAnchor != w.ParentAnchor ||
			!samePath(g.ParentPath, w.ParentPath) {
			t.Errorf("crumb %d = %+v, want %+v", i, g, w)
		}
	}
}

// TestLayoutOuterViewportsStaySessionOnly: the frames a pane would ascend
// through keep their identity in the blob, never their viewports — those are
// session-only, and a restored ascent falls back to each grid's persisted
// framing (Frame.HasView).
func TestLayoutOuterViewportsStaySessionOnly(t *testing.T) {
	tr := NewTree()
	p := tr.FocusedPane()
	p.Stack = NewStack("plugin/1")
	p.Cx, p.Cy, p.Zoom = 11, 22, 3
	p.Push(Frame{GridID: "other/1", Door: "plugin/9", Zoom: 1})

	data, _, err := EncodeLayout(tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("11")) || bytes.Contains(data, []byte("22")) {
		t.Fatalf("an outer viewport leaked into the blob: %s", data)
	}
	got, err := DecodeLayout(data, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	gp := got.FocusedPane()
	if !gp.Pop() {
		t.Fatal("restored pane cannot ascend")
	}
	if gp.HasView() {
		t.Fatalf("restored outer frame carries a viewport: %+v", gp.Frame)
	}
}

// TestLayoutOmitsPlaceWhenTheProjectionHoldsIt: a place with no crossing
// below its top level encodes exactly as it always did, so visiting an
// existing workspace re-encodes byte-identically and the persister's diff
// stays quiet. Reading never mutates.
func TestLayoutOmitsPlaceWhenTheProjectionHoldsIt(t *testing.T) {
	tr := NewTree()
	p := tr.FocusedPane()
	p.Stack = StackAt("plugin/1", []string{"plugin/4", "plugin/7"}, "plugin/12")
	p.Zoom = 1

	data, _, err := EncodeLayout(tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"place"`)) {
		t.Fatalf("place written for a single-namespace stack: %s", data)
	}
}

// TestEncoderAlwaysWritesTextFocus: TextFocus is the server's one record of a
// leaf's content descent (panelayout.TextFocusIDs reads it, for both the boot
// sweep's protection set and the delete-time reap), so the encoder must write
// it for every content descent — including the deep places where it also
// writes the full Place stack, whose top frame says the same thing.
func TestEncoderAlwaysWritesTextFocus(t *testing.T) {
	tr := NewTree()
	p := tr.FocusedPane()
	p.Stack = NewStack("plugin/1")
	p.Push(Frame{GridID: "other/1", Door: "plugin/9", Zoom: 1})
	p.Push(Frame{Door: "other/12", Content: true})
	if p.ProjectionHolds() {
		t.Fatal("fixture no longer forces Place; pick a deeper place")
	}

	data, _, err := EncodeLayout(tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"place"`)) {
		t.Fatalf("expected the full place stack in the blob: %s", data)
	}
	ids, err := panelayout.TextFocusIDs(data)
	if err != nil {
		t.Fatalf("TextFocusIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "other/12" {
		t.Errorf("the server sees %v referenced, want [other/12]: %s", ids, data)
	}
}

// IDPrefix round trip: decode applies the level namespace to every pane id,
// Focus and Zoomed included, and minting follows. Encode strips it, so the
// stored blob is byte-identical to a bare tree's.
func TestLayoutIDPrefixRoundTrip(t *testing.T) {
	src := NewTree()
	p1 := src.FocusedPane()
	p1.Stack = StackAt("aabb/1", nil, "")
	if _, err := src.Split(Vertical); err != nil {
		t.Fatal(err)
	}
	bare, _, err := EncodeLayout(src, nil)
	if err != nil {
		t.Fatal(err)
	}

	dec, err := DecodeLayout(bare, nil, "w2:")
	if err != nil {
		t.Fatal(err)
	}
	dec.Walk(func(p *Pane) {
		if !strings.HasPrefix(p.ID, "w2:p") {
			t.Errorf("pane id %q not namespaced", p.ID)
		}
	})
	if !strings.HasPrefix(dec.Focus, "w2:") {
		t.Errorf("focus %q not namespaced", dec.Focus)
	}
	// Minting inside the prefixed tree stays inside the namespace.
	np, err := dec.Split(Vertical)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(np.ID, "w2:p") {
		t.Errorf("minted id %q not namespaced", np.ID)
	}

	// Encoding the prefixed tree writes BARE ids — the durable blob never
	// carries a session namespace.
	out, _, err := EncodeLayout(dec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "w2:") {
		t.Fatalf("blob leaked the namespace: %s", out)
	}
	rt, err := DecodeLayout(out, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	rt.Walk(func(p *Pane) {
		if strings.Contains(p.ID, "w2:") {
			t.Errorf("bare re-decode kept a namespace: %q", p.ID)
		}
	})
}

// goldenLeaf / goldenTextLeaf build the expected decode of the golden blob:
// a place is constructed through StackAt, the one decoder, never by poking
// fields.
func goldenLeaf(id, anchor string, path []string, content string, cx, cy, zoom float64) *Pane {
	p := &Pane{ID: id, Stack: StackAt(anchor, path, content)}
	p.Cx, p.Cy, p.Zoom = cx, cy, zoom
	return p
}

func goldenTextLeaf(id, anchor, content string) *Pane {
	p := goldenLeaf(id, anchor, nil, content, 1, 2, 1)
	p.TextMode = "rendered"
	p.TextScrollX, p.TextScrollY, p.TextZoom = 10, 80, 1.25
	return p
}
