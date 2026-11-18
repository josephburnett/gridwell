package server_test

import (
	"context"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
)

// Two nodes that declare each other answer a client's handshake by asking
// each far node only for its home's facts, never its roster: a roster holds
// the far node's own connections, which would ask back.
func TestMutualConnectionsHandshakeOnceAcross(t *testing.T) {
	ctx := context.Background()
	m := newMesh(t, []string{meNode, aNode}, []meshEdge{
		{meNode, "toa", aNode},
		{aNode, "tome", meNode},
	})
	for _, n := range []string{meNode, aNode} {
		if _, err := m.nodes[n].cl.Handshake(ctx); err != nil {
			t.Fatal(err)
		}
	}
	toa, tome := m.asks(meNode, "toa"), m.asks(aNode, "tome")
	before := toa.handshakes.Load() + toa.infos.Load() + tome.handshakes.Load() + tome.infos.Load()

	start := time.Now()
	hs, err := m.nodes[meNode].cl.Handshake(ctx)
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	crossed := toa.handshakes.Load() + toa.infos.Load() + tome.handshakes.Load() + tome.infos.Load() - before
	if crossed > 2 || took > 300*time.Millisecond {
		t.Fatalf("one client handshake took %v and sent %d calls across the two connections, want at most 2 and well under a second", took, crossed)
	}
	var row bool
	for _, s := range hs.Sources {
		if s.Uuid == rpc.QualifyID(meNode, "toa") && s.RootGridId == m.home(meNode, aNode, "toa") {
			row = true
		}
	}
	if !row {
		t.Fatalf("the roster names no landing for toa: %v", hs.Sources)
	}
}
