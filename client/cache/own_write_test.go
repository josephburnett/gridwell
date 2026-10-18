package cache

import (
	"bytes"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
)

func paneRow(blob int64) *gridwellv1.Tile {
	return &gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindWorkspace, Version: 1, BlobId: blob}
}

// seedLayout holds a pane row at blob 7 and its layout "A", read clean.
func seedLayout(t *testing.T) *Cache {
	t.Helper()
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{paneRow(7)})
	c.PutFetchedContent("10", []byte("A"), vb(1), c.AskContent("10"))
	return c
}

// A write's own echo can overtake its response. While the write is in flight
// the echo is not news about the body, so the body stays and nothing reads the
// tile again for the bytes this client just sent.
func TestAnOwnWriteEchoLeadingItsResponseKeepsTheBody(t *testing.T) {
	c := seedLayout(t)
	gen := c.BodyGen("10")
	end := c.BeginWrite("10")
	c.Apply(changedEvent(paneRow(8)))
	if _, ok := c.TileContent("10"); !ok {
		t.Fatal("the write's own echo dropped the body, so the next draw reads back what this client just wrote")
	}
	c.PutSavedContent(paneRow(8), []byte("B"))
	c.PutWriteResponse("1", paneRow(8), WroteBody)
	end()
	got, ok := c.TileContent("10")
	if !ok || !bytes.Equal(got, []byte("B")) {
		t.Fatalf("after the response the body is %q (held %v), want the written B", got, ok)
	}
	if c.BodyGen("10") == gen {
		t.Error("the bytes changed and BodyGen did not move: a picture of A outlives it")
	}
}

// A foreign change that lands while a write is in flight is still news: when
// the write ends without filing bytes, the body is aged against the row the
// cache then holds.
func TestAForeignChangeDuringAWriteStillAgesTheBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  *gridwellv1.Tile
		told bool
	}{
		{"a home row moved past the body", &gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 2}, false},
		{"a claimless row an event says changed", &gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New()
			seed := proto0(tc.row)
			c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{seed})
			c.PutFetchedContent("10", []byte("A"), rpc.BasisOf(seed), c.AskContent("10"))
			end := c.BeginWrite("10")
			c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: tc.row, ContentChanged: tc.told}}})
			end()
			if _, ok := c.TileContent("10"); ok {
				t.Fatal("a foreign change during a failed write left the old body standing")
			}
		})
	}
}

// proto0 is the row before tc's change: version 1 for a home row, the same
// claimless row for a plugin's.
func proto0(n *gridwellv1.Tile) *gridwellv1.Tile {
	seed := &gridwellv1.Tile{Id: n.Id, GridId: n.GridId, Kind: n.Kind}
	if n.Version > 0 {
		seed.Version = 1
	}
	return seed
}

// The same holds for a row whose grid this cache does not hold.
func TestAForeignRowOutsideTheCacheDuringAWriteStillAgesTheBody(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "2"}, []*gridwellv1.Tile{{Id: "10", GridId: "2", Kind: rpc.KindText, Version: 1}})
	c.PutFetchedContent("10", []byte("A"), vb(1), c.AskContent("10"))
	end := c.BeginWrite("10")
	c.Apply(changedEvent(&gridwellv1.Tile{Id: "10", GridId: "9", Kind: rpc.KindText, Version: 2}))
	end()
	if _, ok := c.TileContent("10"); ok {
		t.Fatal("a newer row for a grid not cached left the old body standing")
	}
}

// A tile with no body yet is not read while a write to it is in flight: the
// response files the bytes the read would ask for.
func TestATileBeingWrittenIsNotAskedFor(t *testing.T) {
	c := New()
	end := c.BeginWrite("10")
	if !c.Writing("10") {
		t.Fatal("a write in flight is not reported")
	}
	end()
	if c.Writing("10") {
		t.Fatal("an ended write is still reported")
	}
}

// Two writes in flight on one tile hold it until both end.
func TestOverlappingWritesHoldUntilTheLastEnds(t *testing.T) {
	c := seedLayout(t)
	end1, end2 := c.BeginWrite("10"), c.BeginWrite("10")
	c.Apply(changedEvent(paneRow(8)))
	end1()
	if _, ok := c.TileContent("10"); !ok {
		t.Fatal("ending one write released the hold the other still needs")
	}
	c.PutSavedContent(paneRow(8), []byte("B"))
	end2()
	end2()
	if _, ok := c.TileContent("10"); !ok {
		t.Fatal("the last write filed its bytes under the row the cache holds, and they were dropped")
	}
}
