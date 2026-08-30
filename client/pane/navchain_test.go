package pane

import (
	"reflect"
	"testing"
)

func TestNavChainOutsideAnyView(t *testing.T) {
	var s Levels
	p := &Pane{Stack: StackAt("u1/0", []string{"w1"}, "")}
	got := s.NavChain(p)
	if len(got) != 2 || got[0].CloseOnly || got[0].PaneTile || got[0].Crumb.Anchor != "u1/0" || got[1].Crumb.TileID != "w1" {
		t.Fatalf("session tree: chain = %+v", got)
	}
	if s.NavChain(nil) != nil {
		t.Fatal("no pane, no crumbs")
	}
}

// Inside two nested levels: the root crumb (close-all, wearing the origin's
// root face), one boundary crumb per level in stack order, then the live
// pane's own chain — never the intermediate trees' chains.
func TestNavChainInsideViews(t *testing.T) {
	outer := NewTree()
	op := outer.FocusedPane()
	op.Stack = StackAt("u1/0", []string{"deep1", "deep2"}, "")
	var s Levels
	s.Push(Level{OuterTree: outer, OriginPane: op.ID, TileID: "pt1"})
	s.Push(Level{TileID: "pt2"}) // boot-restored: no outer tree
	live := &Pane{Stack: StackAt("u2/0", []string{"x"}, "")}
	got := s.NavChain(live)
	want := []NavCrumb{
		{CloseOnly: true, Crumb: op.Crumbs()[0]},
		{PaneTile: true, WsLevel: 1, TileID: "pt1"},
		{PaneTile: true, WsLevel: 2, TileID: "pt2"},
		{Crumb: live.Crumbs()[0]},
		{Crumb: live.Crumbs()[1]},
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
	var boot Levels
	boot.Push(Level{TileID: "pt"})
	if r := boot.NavChain(nil)[0]; !r.CloseOnly || !reflect.DeepEqual(r.Crumb, Crumb{}) {
		t.Fatalf("boot root crumb = %+v", r)
	}
}
