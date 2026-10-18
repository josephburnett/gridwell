package server

import (
	"bytes"
	"context"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
)

// A pane layout write's own echo, off the real Subscribe stream, applied
// before its real response the way the client sees it whenever the stream
// beats the reply: the body this client just wrote must stay cached, because
// a missing body is what makes the next draw ReadContent the tile.
func TestALayoutWriteEchoLeadingItsResponseIsNotReadBack(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	before := []byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`)
	after := []byte(`{"v":1,"root":{"pane":{"id":"p2","zoom":1}},"focus":"p2"}`)
	pt, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindWorkspace, X: 0, Y: 0, W: 2, H: 2}}, before)
	if err != nil {
		t.Fatal(err)
	}
	grid, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	c := cache.New()
	c.PutGrid(grid.Grid, grid.Tiles)
	asked := c.AskContent(pt.Id)
	data, _, basis, err := cl.ReadContent(ctx, pt.Id)
	if err != nil {
		t.Fatal(err)
	}
	c.PutFetchedContent(pt.Id, data, basis, asked)
	gen := c.BodyGen(pt.Id)

	echoes := make(chan *gridwellv1.Tile, 32)
	stream, err := cl.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer stream.Close()
		for {
			ev, ok, err := stream.Recv()
			if err != nil || !ok {
				return
			}
			if ch := ev.GetTileChanged(); ch != nil && ch.GetTile().GetId() == pt.Id {
				echoes <- ch.Tile
			}
		}
	}()

	// As App.postPaneLayout sends it.
	end := c.BeginWrite(pt.Id)
	resp, err := cl.WriteContent(ctx, pt.Id, basis, after)
	if err != nil {
		t.Fatal(err)
	}
	var echo *gridwellv1.Tile
	for echo == nil || echo.BlobId != resp.BlobId {
		select {
		case echo = <-echoes:
		case <-ctx.Done():
			t.Fatal("the write's echo never arrived")
		}
	}
	if echo.BlobId == grid.Tiles[0].GetBlobId() {
		t.Fatal("the layout write did not move the blob; the test no longer exercises the echo")
	}

	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: echo}}})
	if _, ok := c.TileContent(pt.Id); !ok {
		t.Fatal("the write's own echo dropped the body: the next draw reads back the layout this client just wrote")
	}
	c.PutSavedContent(resp, after)
	c.PutWriteResponse(resp.GridId, resp, cache.WroteBody)
	end()
	got, ok := c.TileContent(pt.Id)
	if !ok || !bytes.Equal(got, after) {
		t.Fatalf("settled body %q (held %v), want the written layout", got, ok)
	}
	if c.BodyGen(pt.Id) == gen {
		t.Error("the bytes changed and BodyGen did not move")
	}
}
