package localdb_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The content-stream seam tests run over REAL gRPC (plugin.ServeInProcess),
// because the property under test — commit-at-close, and a broken stream
// committing nothing — lives in the stream lifecycle, which a direct method
// call cannot exercise.

func serveGRPC(t *testing.T) (gridwellv1.GridwellClient, string) {
	t.Helper()
	p := openPlugin(t)
	root := rootGrid(t, p)
	client, closer, err := compose.ServeInProcess(p)
	if err != nil {
		t.Fatalf("serve in-process: %v", err)
	}
	t.Cleanup(closer)
	return client, root
}

func grpcCreateText(t *testing.T, c gridwellv1.GridwellClient, gridID string, body []byte) *gridwellv1.Tile {
	t.Helper()
	r, err := c.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{
		GridId: gridID,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 4, H: 4},
	})
	if err != nil {
		t.Fatalf("CreateTile: %v", err)
	}
	tile := r.Tile
	if len(body) > 0 {
		// Creation is metadata-only; the body follows through the one write.
		w, err := c.WriteContent(context.Background())
		if err != nil {
			t.Fatalf("WriteContent open: %v", err)
		}
		if err := w.Send(&gridwellv1.WriteContentRequest{TileId: tile.Id, Version: tile.Version, Data: body}); err != nil {
			t.Fatalf("WriteContent send: %v", err)
		}
		resp, err := w.CloseAndRecv()
		if err != nil {
			t.Fatalf("WriteContent close: %v", err)
		}
		tile = resp.Tile
	}
	return tile
}

// readAll drains a ReadContent stream, returning the reassembled bytes and
// the meta from chunk 1.
func readAll(t *testing.T, c gridwellv1.GridwellClient, tileID string) (data []byte, mediaType string, version int64, chunks int) {
	t.Helper()
	stream, err := c.ReadContent(context.Background(), &gridwellv1.ReadContentRequest{TileId: tileID})
	if err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	for {
		ch, err := stream.Recv()
		if err == io.EOF {
			return data, mediaType, version, chunks
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		chunks++
		if chunks == 1 {
			mediaType, version = ch.MediaType, ch.Version
		} else if ch.MediaType != "" || ch.Version != 0 {
			t.Errorf("chunk %d carries meta (media %q, version %d); meta rides chunk 1 only", chunks, ch.MediaType, ch.Version)
		}
		data = append(data, ch.Data...)
	}
}

func TestContentStreamRoundTrip(t *testing.T) {
	c, root := serveGRPC(t)
	tile := grpcCreateText(t, c, root, []byte("old"))

	// Write in several chunks; the value commits at CloseAndRecv.
	w, err := c.WriteContent(context.Background())
	if err != nil {
		t.Fatalf("WriteContent open: %v", err)
	}
	if err := w.Send(&gridwellv1.WriteContentRequest{TileId: tile.Id, Version: tile.Version, Data: []byte("# Streamed\n")}); err != nil {
		t.Fatalf("send 1: %v", err)
	}
	if err := w.Send(&gridwellv1.WriteContentRequest{Data: []byte("part two, ")}); err != nil {
		t.Fatalf("send 2: %v", err)
	}
	if err := w.Send(&gridwellv1.WriteContentRequest{Data: []byte("part three")}); err != nil {
		t.Fatalf("send 3: %v", err)
	}
	resp, err := w.CloseAndRecv()
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if resp.Tile.Version <= tile.Version {
		t.Errorf("text write must bump version: %d -> %d", tile.Version, resp.Tile.Version)
	}

	want := []byte("# Streamed\npart two, part three")
	data, media, version, _ := readAll(t, c, tile.Id)
	if !bytes.Equal(data, want) {
		t.Errorf("read back %q, want %q", data, want)
	}
	if version != resp.Tile.Version {
		t.Errorf("bytes paired with version %d, row at %d — basis must never split", version, resp.Tile.Version)
	}
	if media == "" {
		t.Error("chunk 1 must carry the media type")
	}
}

// TestWriteContentBrokenStreamCommitsNothing is the commit-at-close seam
// test: a stream that dies mid-write must leave the old value byte-for-byte
// intact — partial delivery is never visible. (The value-vs-wire split:
// content is a value; a torn write would be corruption.)
func TestWriteContentBrokenStreamCommitsNothing(t *testing.T) {
	c, root := serveGRPC(t)
	tile := grpcCreateText(t, c, root, []byte("the old value"))

	ctx, cancel := context.WithCancel(context.Background())
	w, err := c.WriteContent(ctx)
	if err != nil {
		t.Fatalf("WriteContent open: %v", err)
	}
	_ = w.Send(&gridwellv1.WriteContentRequest{TileId: tile.Id, Version: tile.Version, Data: []byte("half a new va")})
	cancel() // the stream breaks before close: no commit
	if _, err := w.CloseAndRecv(); err == nil {
		t.Fatal("a cancelled write must not report success")
	}

	data, _, version, _ := readAll(t, c, tile.Id)
	if !bytes.Equal(data, []byte("the old value")) {
		t.Errorf("broken stream tore the value: %q", data)
	}
	if version != tile.Version {
		t.Errorf("broken stream moved the version: %d -> %d", tile.Version, version)
	}
}

func TestReadContentChunksLargeBodies(t *testing.T) {
	c, root := serveGRPC(t)
	big := bytes.Repeat([]byte("0123456789abcdef"), 40*1024) // 640 KiB > 2 chunks
	tile := grpcCreateText(t, c, root, nil)

	w, err := c.WriteContent(context.Background())
	if err != nil {
		t.Fatalf("WriteContent open: %v", err)
	}
	if err := w.Send(&gridwellv1.WriteContentRequest{TileId: tile.Id, Version: tile.Version, Data: big}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := w.CloseAndRecv(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, _, _, chunks := readAll(t, c, tile.Id)
	if !bytes.Equal(data, big) {
		t.Errorf("large body corrupted in chunking: %d bytes back, want %d", len(data), len(big))
	}
	if chunks < 3 {
		t.Errorf("640 KiB should stream in >= 3 chunks, got %d", chunks)
	}
}
