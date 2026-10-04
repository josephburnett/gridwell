package rpc

import (
	"errors"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// me → toa → na → toc → nc; me → tob → nb; na → tob2 → nb. Nothing points
// back at me, and nb declares nothing.
func branchReach() Reach {
	return Reach{
		"nodeme1": {Edges: []ReachEdge{{Conn: "toa", Lands: "nodea01"}, {Conn: "tob", Lands: "nodeb01"}}},
		"nodea01": {Edges: []ReachEdge{{Conn: "toc", Lands: "nodec01"}, {Conn: "tob2", Lands: "nodeb01"}}},
		"nodeb01": {},
		"nodec01": {},
	}
}

func TestChainOfSplitsTheHopsFromTheTail(t *testing.T) {
	cases := []struct {
		id           string
		conns        []string
		prefix, tail string
	}{
		{"nodeme1/5", nil, "", "nodeme1/5"},
		{"p9xyzab/5", nil, "", "p9xyzab/5"},
		{"nodeme1/toa/nodea01/5", []string{"toa"}, "nodeme1/toa/", "nodea01/5"},
		{"nodeme1/toa/p9xyzab/5", []string{"toa"}, "nodeme1/toa/", "p9xyzab/5"},
		{"nodeme1/toa/nodea01/toc/nodec01/5", []string{"toa", "toc"}, "nodeme1/toa/nodea01/toc/", "nodec01/5"},
		// Under another node's id nothing is a hop of this node's.
		{"nodexxx/toa/nodea01/5", nil, "", "nodexxx/toa/nodea01/5"},
	}
	for _, c := range cases {
		conns, prefix, tail := ChainOf(c.id, "nodeme1")
		if len(conns) != len(c.conns) || prefix != c.prefix || tail != c.tail || prefix+tail != c.id {
			t.Errorf("ChainOf(%q) = %v %q %q, want %v %q %q", c.id, conns, prefix, tail, c.conns, c.prefix, c.tail)
			continue
		}
		for i := range conns {
			if conns[i] != c.conns[i] {
				t.Errorf("ChainOf(%q) conns = %v, want %v", c.id, conns, c.conns)
			}
		}
	}
}

func TestReachEdgesReadsWhereEachConnectionLands(t *testing.T) {
	lp := &pb.HandshakeResponse{Plugins: []*pb.PluginInfo{
		{Uuid: "nodeme1/toa/nodea01", RootGridId: "nodeme1/toa/nodea01/1"},
		ConnectionRow("nodeme1/toa/nodea01/toc", "C", "nodeme1/toa/nodea01/toc/nodec01/1", "", View{}),
		ConnectionRow("nodeme1/toa/nodea01/pending", "P", "", "dialing", View{}),
	}}
	got := ReachEdges(lp)
	if len(got) != 1 || got[0] != (ReachEdge{Conn: "toc", Lands: "nodec01", Label: "C"}) {
		t.Fatalf("ReachEdges = %+v, want the one landed connection", got)
	}
}

// A reference is spelled from the node that holds it, along connections that
// node declares; with no such chain it cannot be held.
func TestRespellSpellsFromTheHolderOrRefuses(t *testing.T) {
	r := branchReach()
	cases := []struct {
		grid, ref, want string
		refused         *Unreachable
	}{
		// On the holder itself: the peel alone is right.
		{"nodeme1/toa/nodea01/1", "nodeme1/toa/nodea01/5", "nodeme1/toa/nodea01/5", nil},
		// Beyond the holder on the same path.
		{"nodeme1/toa/nodea01/1", "nodeme1/toa/nodea01/toc/nodec01/5", "nodeme1/toa/nodea01/toc/nodec01/5", nil},
		// Another branch, which the holder reaches under its own name.
		{"nodeme1/toa/nodea01/1", "nodeme1/tob/nodeb01/5", "nodeme1/toa/nodea01/tob2/nodeb01/5", nil},
		{"nodeme1/toa/nodea01/1", "nodeme1/tob/p9xyzab/5", "nodeme1/toa/nodea01/tob2/p9xyzab/5", nil},
		// Two hops down, the holder is c, which reaches nothing.
		{"nodeme1/toa/nodea01/toc/nodec01/1", "nodeme1/tob/nodeb01/5", "", &Unreachable{Holder: "nodec01", Target: "nodeb01"}},
		// Nothing points back at me.
		{"nodeme1/toa/nodea01/1", "nodeme1/5", "", &Unreachable{Holder: "nodea01", Target: "nodeme1"}},
		{"nodeme1/tob/nodeb01/1", "nodeme1/toa/nodea01/5", "", &Unreachable{Holder: "nodeb01", Target: "nodea01"}},
		// A target through a connection nobody declares cannot be placed.
		{"nodeme1/toa/nodea01/1", "nodeme1/gone/nodeb01/5", "", &Unreachable{Holder: "nodea01"}},
	}
	for _, c := range cases {
		got, err := r.Respell("nodeme1", c.grid, c.ref, 8)
		if c.refused != nil {
			var u *Unreachable
			if !errors.As(err, &u) || *u != *c.refused {
				t.Errorf("Respell(%q, %q) = %q, %v; want refused %+v", c.grid, c.ref, got, err, c.refused)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("Respell(%q, %q) = %q, %v; want %q", c.grid, c.ref, got, err, c.want)
		}
	}
}

func TestRouteIsBoundedAndSurvivesCycles(t *testing.T) {
	r := Reach{
		"n1": {Edges: []ReachEdge{{Conn: "x", Lands: "n2"}}},
		"n2": {Edges: []ReachEdge{{Conn: "back", Lands: "n1"}, {Conn: "y", Lands: "n3"}}},
		"n3": {Edges: []ReachEdge{{Conn: "z", Lands: "n4"}}},
	}
	if p, ok := r.Route("n1", "n4", 3); !ok || len(p) != 3 {
		t.Fatalf("Route n1→n4 = %v %v, want three hops", p, ok)
	}
	if _, ok := r.Route("n1", "n4", 2); ok {
		t.Fatal("a route longer than the cap must not be found")
	}
	if _, ok := r.Route("n1", "n9", 8); ok {
		t.Fatal("a node nobody reaches must not be found")
	}
}

func TestHeldAcrossOnlyWhereTheGridIsBehindAnotherConnection(t *testing.T) {
	cases := []struct {
		grid, ref string
		want      bool
	}{
		{"nodeme1/1", "nodeme1/tob/nodeb01/5", false},
		{"p9xyzab/1", "nodeme1/5", false},
		{"nodeme1/toa/nodea01/1", "nodeme1/toa/nodea01/toc/nodec01/5", false},
		{"nodeme1/toa/nodea01/1", "nodeme1/tob/nodeb01/5", true},
		{"nodeme1/toa/nodea01/1", "nodeme1/5", true},
		{"nodeme1/toa/nodea01/1", "p9xyzab/5", true},
		{"nodeme1/toa/nodea01/1", "5", false},
		{"nodeme1/toa/nodea01/1", "", false},
	}
	for _, c := range cases {
		if got := HeldAcross(c.grid, c.ref, "nodeme1"); got != c.want {
			t.Errorf("HeldAcross(%q, %q) = %v, want %v", c.grid, c.ref, got, c.want)
		}
	}
}
