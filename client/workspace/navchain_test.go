package workspace

import (
	"reflect"
	"testing"

	"github.com/josephburnett/gridwell/client/pane"
)

func TestNavChainOutsideAnyView(t *testing.T) {
	var s Stack
	p := &pane.Pane{Anchor: "u1/0", Path: []string{"w1"}}
	got := s.NavChain(p)
	if len(got) != 2 || got[0].CloseOnly || got[0].PaneTile || got[0].Crumb.Anchor != "u1/0" || got[1].Crumb.TileID != "w1" {
		t.Fatalf("session tree: chain = %+v", got)
	}
	if s.NavChain(nil) != nil {
		t.Fatal("no pane, no crumbs")
	}
}

// Inside two nested views: ROOT (close-all, wearing the origin's root
// face), one boundary crumb per view in stack order, then the live pane's
// own chain — and never the intermediate trees' chains (#245 tweak).
func TestNavChainInsideViews(t *testing.T) {
	outer := pane.NewTree()
	op := outer.FocusedPane()
	op.Anchor = "u1/0"
	op.Path = []string{"deep1", "deep2"}
	var s Stack
	s.Push(Frame{OuterTree: outer, OriginPane: op.ID, TileID: "pt1"})
	s.Push(Frame{TileID: "pt2"}) // boot-restored: no outer tree
	live := &pane.Pane{Anchor: "u2/0", Path: []string{"x"}}
	got := s.NavChain(live)
	want := []NavCrumb{
		{CloseOnly: true, Crumb: pane.DescentChain(op)[0]},
		{PaneTile: true, WsLevel: 1, TileID: "pt1"},
		{PaneTile: true, WsLevel: 2, TileID: "pt2"},
		{Crumb: pane.DescentChain(live)[0]},
		{Crumb: pane.DescentChain(live)[1]},
	}
	if len(got) != len(want) {
		t.Fatalf("chain = %+v", got)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("crumb %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// A boot-restored innermost frame: the root crumb has no face.
	var boot Stack
	boot.Push(Frame{TileID: "pt"})
	if r := boot.NavChain(nil)[0]; !r.CloseOnly || !reflect.DeepEqual(r.Crumb, pane.Crumb{}) {
		t.Fatalf("boot root crumb = %+v", r)
	}
}
