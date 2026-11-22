package pane

import (
	"strings"
	"testing"
)

// The ids of Joe's trace of 2026-11-22, in shape: the laptop holds a
// connection to rtb, and rtb one to a third node.
const (
	lap    = "lapnode"
	lapRTB = lap + "/connrtb/rtbnode/"
	rtbSSS = lapRTB + "connsss/sssnode/"
)

// joesPanes are the four panes of the trace's last view (seq 12333), each
// reached from the laptop's home through its door 1519 onto rtb's home.
func joesPanes(t *testing.T) *Tree {
	t.Helper()
	viaLaptop := func() Stack {
		s := StackOf([]Frame{viewed(Frame{GridID: lap + "/1"}, 3, 4, 1)})
		s.Push(viewed(Frame{GridID: lapRTB + "1", Door: lap + "/1519"}, 5, 6, 2))
		return s
	}
	tr := NewTree()
	p8 := tr.FocusedPane()
	p8.Stack = viaLaptop()
	p8.Push(viewed(Frame{GridID: lapRTB + "1156", Door: lapRTB + "40"}, 7, 8, 1))
	p9, err := tr.Split(Vertical)
	if err != nil {
		t.Fatal(err)
	}
	p9.Stack = viaLaptop()
	p9.Push(viewed(Frame{GridID: lapRTB + "1155", Door: lapRTB + "41"}, 1, 1, 1))
	p9.Push(viewed(Frame{Door: lapRTB + "2307", Content: true}, 2, 2, 1))
	p11, err := tr.Split(Horizontal)
	if err != nil {
		t.Fatal(err)
	}
	p11.Stack = viaLaptop()
	p6, err := tr.Split(Vertical)
	if err != nil {
		t.Fatal(err)
	}
	p6.Stack = viaLaptop()
	p6.Push(viewed(Frame{GridID: lapRTB + "1155", Door: lapRTB + "41"}, 1, 1, 1))
	p6.Push(viewed(Frame{Door: lapRTB + "2297"}, 9, 9, 3))
	return tr
}

// Joe's capture: a workspace tile on rtb, made from a window whose every
// pane came through the laptop's own home, keeps all four panes, each showing
// what it showed, with the laptop's frames below rtb's left out.
func TestAFarWorkspaceKeepsPanesReachedThroughTheNearNode(t *testing.T) {
	tr := joesPanes(t)
	data, skipped, err := EncodeLayout(tr, OwnerFrame(lapRTB+"2317"))
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Fatalf("panes dropped: %v", skipped)
	}
	if strings.Contains(string(data), lap) {
		t.Fatalf("the blob names the laptop: %s", data)
	}
	got, err := DecodeLayout(data, func(id string) string { return lap + "/connrtb/" + id }, "")
	if err != nil {
		t.Fatal(err)
	}
	tr.Walk(func(want *Pane) {
		gp := got.FindPane(want.ID)
		if gp == nil {
			t.Fatalf("pane %s is gone", want.ID)
		}
		wf, gf := want.Frames()[2:], gp.Frames()
		wf = append([]Frame{want.Frames()[1]}, wf...)
		if len(gf) != len(wf) {
			t.Fatalf("pane %s has %d frames, want %d: %+v", want.ID, len(gf), len(wf), gp.Crumbs())
		}
		if gp.Anchor() != want.Anchor() || !samePath(gp.Path(), want.Path()) || gp.ContentID() != want.ContentID() {
			t.Errorf("pane %s shows %q %v %q, want %q %v %q", want.ID, gp.Anchor(), gp.Path(), gp.ContentID(),
				want.Anchor(), want.Path(), want.ContentID())
		}
		if c := gp.Crumbs(); c[0].Anchor != lapRTB+"1" || c[0].TileID != "" {
			t.Errorf("pane %s's first crumb is %+v, want rtb's home", want.ID, c[0])
		}
		for i := range gf {
			if gf[i].Door != wf[i].Door && i > 0 || gf[i].GridID != wf[i].GridID ||
				!gf[i].Content && gf[i].View != wf[i].View {
				t.Errorf("pane %s frame %d is %+v, want %+v", want.ID, i, gf[i], wf[i])
			}
		}
	})
}

// allFarPanes is a window whose every frame is rtb's: a pane opened from
// rtb's swatch, descended through a door, a link and into a text.
func allFarPanes(t *testing.T) *Tree {
	t.Helper()
	tr := NewTree()
	a := tr.FocusedPane()
	a.Stack = StackOf([]Frame{viewed(Frame{GridID: lapRTB + "1"}, 5, 6, 2)})
	a.Push(viewed(Frame{Door: lapRTB + "12"}, 1, 2, 1))
	a.Push(viewed(Frame{GridID: lapRTB + "1155", Door: lapRTB + "41"}, 3, 3, 1))
	a.Push(viewed(Frame{Door: lapRTB + "2307", Content: true}, 2, 2, 1))
	b, err := tr.Split(Vertical)
	if err != nil {
		t.Fatal(err)
	}
	b.Stack = StackAt(lapRTB+"1", []string{lapRTB + "12"}, "")
	b.SetView(7, 7, 3)
	return tr
}

// A workspace whose every frame its owner names encodes byte for byte as it
// did before Respelled: these bytes are the encoder's at cbf41b52.
func TestAnAllNamedFarLayoutEncodesAsBefore(t *testing.T) {
	const want = `{"v":1,"root":{"split":{"dir":"v","ratio":0.5,"a":{"pane":{"id":"p1","anchor":"rtbnode/1155","text_focus":"rtbnode/2307","place":[{"g":"rtbnode/1"},{"d":"rtbnode/12"},{"d":"rtbnode/41","g":"rtbnode/1155"},{"d":"rtbnode/2307","c":true}],"views":[{"x":5,"y":6,"z":2},{"x":1,"y":2,"z":1},{"x":3,"y":3,"z":1},null]}},"b":{"pane":{"id":"p2","anchor":"rtbnode/1","path":["rtbnode/12"],"views":[null,{"x":7,"y":7,"z":3}]}}}},"focus":"p1"}`
	data, skipped, err := EncodeLayout(allFarPanes(t), OwnerFrame(lapRTB+"2317"))
	if err != nil || len(skipped) != 0 {
		t.Fatalf("err=%v skipped=%v", err, skipped)
	}
	if string(data) != want {
		t.Fatalf("encoded\n%s\nwant\n%s", data, want)
	}
}

// Respelled leaves out the frames below the lowest grid frame the owner
// names, keeps every frame above it as it was, view and scroll included, and
// hands back a place it names whole untouched.
func TestRespelledDropsOnlyWhatTheOwnerCannotName(t *testing.T) {
	rel := OwnerFrame(lapRTB + "2317")
	p := joesPanes(t).FindPane("p2")
	s, ok := p.Respelled(rel)
	if !ok {
		t.Fatal("Joe's pane is not respelled")
	}
	have, got := p.Frames(), s.Frames()
	if len(got) != len(have)-1 || got[0].GridID != lapRTB+"1" || got[0].Door != "" || got[0].View != have[1].View {
		t.Fatalf("anchor frame %+v from %+v", got[0], have[1])
	}
	for i := 1; i < len(got); i++ {
		if got[i] != have[i+1] {
			t.Errorf("frame %d is %+v, want %+v", i, got[i], have[i+1])
		}
	}

	whole := allFarPanes(t).FocusedPane()
	same, ok := whole.Respelled(rel)
	if !ok || !placesEqual(&Pane{Stack: same}, whole) {
		t.Fatalf("a place the owner names whole came back %+v", same.Crumbs())
	}
	for i, f := range same.Frames() {
		if f != whole.Frames()[i] {
			t.Errorf("frame %d is %+v, want %+v", i, f, whole.Frames()[i])
		}
	}

	// The laptop's own grid and a text on it: nothing for rtb to anchor at.
	mine := StackAt(lap+"/1", []string{lap + "/4"}, lap+"/9")
	if _, ok := mine.Respelled(rel); ok {
		t.Fatal("a place on the laptop alone was respelled")
	}
	// rtb's grid with a link onto a laptop text on top: the shown frame is
	// the laptop's, so there is nothing to keep.
	out := StackAt(lapRTB+"1", nil, "")
	out.Push(Frame{GridID: lap + "/5", Door: lapRTB + "77"})
	out.Push(Frame{Door: lap + "/9", Content: true})
	if _, ok := out.Respelled(rel); ok {
		t.Fatal("a place showing the laptop's text was respelled for rtb")
	}
}

// The rule is one function over any chain: a workspace two connections away
// anchors at the first frame on its node, and one on the client's own node
// keeps a far pane whole.
func TestRespelledHoldsAtEveryDepth(t *testing.T) {
	s := StackAt(lap+"/1", nil, "")
	s.Push(Frame{GridID: lapRTB + "1", Door: lap + "/1519"})
	s.Push(Frame{GridID: rtbSSS + "1", Door: lapRTB + "88"})
	s.Push(Frame{Door: rtbSSS + "3"})
	got, ok := s.Respelled(OwnerFrame(rtbSSS + "50"))
	if !ok || got.Depth() != 2 || got.Anchor() != rtbSSS+"1" || !samePath(got.Path(), []string{rtbSSS + "3"}) {
		t.Fatalf("two hops: %v %+v", ok, got.Crumbs())
	}

	home, ok := s.Respelled(OwnerFrame(lap + "/50"))
	if !ok || !placesEqual(&Pane{Stack: home}, &Pane{Stack: s}) {
		t.Fatalf("a workspace on the laptop changed a far pane: %+v", home.Crumbs())
	}
}

// A place that leaves the owner through a link and comes back keeps the most
// it can: the frames from its last return to the owner up.
func TestRespelledAnchorsAtTheLastReturnToTheOwner(t *testing.T) {
	s := StackAt(lapRTB+"1", []string{lapRTB + "12"}, "")
	s.Push(Frame{GridID: lap + "/5", Door: lapRTB + "77"})
	s.Push(Frame{GridID: lapRTB + "1155", Door: lap + "/9"})
	s.Push(Frame{Door: lapRTB + "2297"})
	got, ok := s.Respelled(OwnerFrame(lapRTB + "2317"))
	if !ok || got.Depth() != 2 || got.Anchor() != lapRTB+"1155" || got.Frames()[0].Door != "" {
		t.Fatalf("re-entry: %v %+v", ok, got.Crumbs())
	}
}

// A pane the owner cannot name at all is the one encode still writes as
// home, and the one it reports, while its siblings are kept.
func TestAPaneTheOwnerCannotNameAtAllIsSkippedAlone(t *testing.T) {
	tr := joesPanes(t)
	tr.FindPane("p3").Stack = StackAt(lap+"/1", []string{lap + "/4"}, "")
	data, skipped, err := EncodeLayout(tr, OwnerFrame(lapRTB+"2317"))
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0] != "p3" {
		t.Fatalf("skipped = %v, want [p3]", skipped)
	}
	got, err := DecodeLayout(data, func(id string) string { return lap + "/connrtb/" + id }, "")
	if err != nil {
		t.Fatal(err)
	}
	if a := got.FindPane("p3").Anchor(); a != "" {
		t.Fatalf("the unnamed pane decodes at %q, want home's empty anchor", a)
	}
	for _, id := range []string{"p1", "p2", "p4"} {
		if got.FindPane(id).Anchor() == "" {
			t.Errorf("pane %s was dropped with p3", id)
		}
	}
}
