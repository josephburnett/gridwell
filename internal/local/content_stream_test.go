package local_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// The content-stream seam tests drive home as the Go value the router holds
// (namespace.Namespace — docs/simplify-plan.md S2). The property under test
// — commit-at-close, and a broken stream committing nothing — lives in the
// stream lifecycle, and the lifecycle is now the recv/send contract: a recv
// that fails must leave the old value byte-for-byte intact.

func homeNamespace(t *testing.T) (namespace.Namespace, string) {
	t.Helper()
	p := openPlugin(t)
	return p, rootGrid(t, p)
}

// sendParts is the caller's half of a WriteContent: the parts in order,
// then io.EOF — the clean end that commits.
func sendParts(parts ...*gridwellv1.WriteContentRequest) func() (*gridwellv1.WriteContentRequest, error) {
	i := 0
	return func() (*gridwellv1.WriteContentRequest, error) {
		if i >= len(parts) {
			return nil, io.EOF
		}
		i++
		return parts[i-1], nil
	}
}

func grpcCreateText(t *testing.T, c namespace.Namespace, gridID string, body []byte) *gridwellv1.Tile {
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
		resp, err := c.WriteContent(context.Background(),
			sendParts(&gridwellv1.WriteContentRequest{TileId: tile.Id, Version: tile.Version, Data: body}))
		if err != nil {
			t.Fatalf("WriteContent: %v", err)
		}
		tile = resp.Tile
	}
	return tile
}

// readAll drains a ReadContent stream, returning the reassembled bytes and
// the meta from chunk 1.
func readAll(t *testing.T, c namespace.Namespace, tileID string) (data []byte, mediaType string, version int64, chunks int) {
	t.Helper()
	err := c.ReadContent(context.Background(), &gridwellv1.ReadContentRequest{TileId: tileID},
		func(ch *gridwellv1.ContentChunk) error {
			chunks++
			if chunks == 1 {
				mediaType, version = ch.MediaType, ch.Version
			} else if ch.MediaType != "" || ch.Version != 0 {
				t.Errorf("chunk %d carries meta (media %q, version %d); meta rides chunk 1 only", chunks, ch.MediaType, ch.Version)
			}
			data = append(data, ch.Data...)
			return nil
		})
	if err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	return data, mediaType, version, chunks
}

func TestContentStreamRoundTrip(t *testing.T) {
	c, root := homeNamespace(t)
	tile := grpcCreateText(t, c, root, []byte("old"))

	// Write in several chunks; the value commits at the clean end.
	resp, err := c.WriteContent(context.Background(), sendParts(
		&gridwellv1.WriteContentRequest{TileId: tile.Id, Version: tile.Version, Data: []byte("# Streamed\n")},
		&gridwellv1.WriteContentRequest{Data: []byte("part two, ")},
		&gridwellv1.WriteContentRequest{Data: []byte("part three")},
	))
	if err != nil {
		t.Fatalf("WriteContent: %v", err)
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
// content is a value; a torn write would be corruption.) The break is a
// recv that FAILS instead of reaching io.EOF — the in-process shape of the
// stream that dropped.
func TestWriteContentBrokenStreamCommitsNothing(t *testing.T) {
	c, root := homeNamespace(t)
	tile := grpcCreateText(t, c, root, []byte("the old value"))

	sent := false
	_, err := c.WriteContent(context.Background(), func() (*gridwellv1.WriteContentRequest, error) {
		if sent {
			return nil, errors.New("the caller's stream broke")
		}
		sent = true
		return &gridwellv1.WriteContentRequest{TileId: tile.Id, Version: tile.Version, Data: []byte("half a new va")}, nil
	})
	if err == nil {
		t.Fatal("a broken write must not report success")
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
	c, root := homeNamespace(t)
	big := bytes.Repeat([]byte("0123456789abcdef"), 40*1024) // 640 KiB > 2 chunks
	tile := grpcCreateText(t, c, root, nil)

	if _, err := c.WriteContent(context.Background(),
		sendParts(&gridwellv1.WriteContentRequest{TileId: tile.Id, Version: tile.Version, Data: big})); err != nil {
		t.Fatalf("WriteContent: %v", err)
	}

	data, _, _, chunks := readAll(t, c, tile.Id)
	if !bytes.Equal(data, big) {
		t.Errorf("large body corrupted in chunking: %d bytes back, want %d", len(data), len(big))
	}
	if chunks < 3 {
		t.Errorf("640 KiB should stream in >= 3 chunks, got %d", chunks)
	}
}
