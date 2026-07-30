package pane

import (
	"reflect"
	"testing"
)

func chainPane() *Pane {
	return &Pane{
		ID:     "p1",
		Anchor: "mnt7abc/9",
		Path:   []string{"4", "7"},
		Up: []Frame{
			{Anchor: "k3x9m2q/1", Path: []string{"12"}},
			{Anchor: "n5aaaaa/1", Path: nil, TextFocus: "33"},
		},
		TextFocus: "21",
	}
}

func TestDescentChainSpansFramesPathAndText(t *testing.T) {
	got := DescentChain(chainPane())
	want := []Crumb{
		{Anchor: "k3x9m2q/1", UpLen: 0, ParentAnchor: "k3x9m2q/1"},
		{TileID: "12", UpLen: 0, PathLen: 1, ParentAnchor: "k3x9m2q/1", ParentPath: []string{}},
		{Anchor: "n5aaaaa/1", UpLen: 1, ParentAnchor: "n5aaaaa/1"},
		{TileID: "33", Text: true, UpLen: 1, PathLen: 0, HasText: true,
			ParentAnchor: "n5aaaaa/1", ParentPath: []string{}},
		{Anchor: "mnt7abc/9", UpLen: 2, ParentAnchor: "mnt7abc/9"},
		{TileID: "4", UpLen: 2, PathLen: 1, ParentAnchor: "mnt7abc/9", ParentPath: []string{}},
		{TileID: "7", UpLen: 2, PathLen: 2, ParentAnchor: "mnt7abc/9", ParentPath: []string{"4"}},
		{TileID: "21", Text: true, UpLen: 2, PathLen: 2, HasText: true,
			ParentAnchor: "mnt7abc/9", ParentPath: []string{"4", "7"}},
	}
	if len(got) != len(want) {
		t.Fatalf("chain length = %d, want %d\n%+v", len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		// slices.Clone of an empty prefix yields an empty non-nil slice;
		// compare by content.
		if g.Anchor != w.Anchor || g.TileID != w.TileID || g.Text != w.Text ||
			g.UpLen != w.UpLen || g.PathLen != w.PathLen || g.HasText != w.HasText ||
			g.ParentAnchor != w.ParentAnchor || !samePath(g.ParentPath, w.ParentPath) {
			t.Errorf("crumb %d = %+v, want %+v", i, g, w)
		}
	}
}

func samePath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	return len(a) == 0 || reflect.DeepEqual(a, b)
}

func TestDescentChainBootBlankIsEmpty(t *testing.T) {
	if c := DescentChain(&Pane{ID: "p1"}); c != nil {
		t.Fatalf("boot-blank pane chain = %+v, want nil", c)
	}
	if c := DescentChain(nil); c != nil {
		t.Fatalf("nil pane chain = %+v, want nil", c)
	}
}

func TestDescentChainRootOnly(t *testing.T) {
	c := DescentChain(&Pane{ID: "p1", Anchor: "k3x9m2q/1"})
	if len(c) != 1 || c[0].Anchor != "k3x9m2q/1" || c[0].TileID != "" {
		t.Fatalf("root-only chain = %+v", c)
	}
}

// The last crumb of any chain IS the pane's current level: never deeper,
// and every earlier crumb is shallower.
func TestDeeperThanOrdersTheChain(t *testing.T) {
	p := chainPane()
	chain := DescentChain(p)
	last := chain[len(chain)-1]
	if DeeperThan(p, last) {
		t.Fatalf("pane deeper than its own innermost crumb")
	}
	for i, c := range chain[:len(chain)-1] {
		if !DeeperThan(p, c) {
			t.Errorf("pane not deeper than crumb %d (%+v)", i, c)
		}
	}
}

// Simulate the ascent loop against the pure pane mutations: each single
// ascent strictly decreases depth, and the loop reaches every crumb.
func TestAscentLoopReachesEveryCrumb(t *testing.T) {
	full := DescentChain(chainPane())
	for target := range full {
		p := chainPane()
		c := full[target]
		for steps := 0; DeeperThan(p, c); steps++ {
			if steps > 16 {
				t.Fatalf("crumb %d: ascent loop did not converge", target)
			}
			ascendOnce(p)
		}
		got := DescentChain(p)
		if len(got) != target+1 {
			t.Errorf("crumb %d: landed at depth %d, want %d", target, len(got), target+1)
		}
		if !reflect.DeepEqual(got[len(got)-1].AtLevel(), c.AtLevel()) {
			t.Errorf("crumb %d: landed on %+v, want %+v", target, got[len(got)-1], c)
		}
	}
}

// AtLevel is the crumb's pane-state shape, for landing comparisons.
func (c Crumb) AtLevel() [3]int { return depthKey(c.UpLen, c.PathLen, c.HasText) }

// ascendOnce mirrors ascendPane's dispatch as pure pane mutations.
func ascendOnce(p *Pane) {
	switch {
	case p.TextFocus != "":
		p.TextFocus = ""
	case len(p.Path) > 0:
		p.Path = p.Path[:len(p.Path)-1]
	case len(p.Up) > 0:
		p.PopFrame()
	}
}

func TestOneAscentReaches(t *testing.T) {
	full := DescentChain(chainPane())
	for target := range full {
		p := chainPane()
		c := full[target]
		for DeeperThan(p, c) {
			if OneAscentReaches(p, c) {
				ascendOnce(p)
				if DeeperThan(p, c) || len(DescentChain(p)) != target+1 {
					t.Fatalf("crumb %d: OneAscentReaches lied — landed at %+v", target, p)
				}
				break
			}
			ascendOnce(p)
		}
	}
	// The innermost crumb is where the pane already is: unreachable by ascent.
	p := chainPane()
	if OneAscentReaches(p, full[len(full)-1]) {
		t.Fatalf("OneAscentReaches claimed the current level")
	}
}
