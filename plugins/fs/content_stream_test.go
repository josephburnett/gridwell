package fs_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/plugins/fs"
)

// The fs half of the Stage 3 seam: ReadContent must serve the same descent
// body GetTileContent does, over a real gRPC stream, and PlaceTile must
// persist a footprint like Move+Resize did.

func TestFSReadContentAndPlaceOverGRPC(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := fs.Open(":memory:", nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	p.SetRoot(dir)

	client, closer, err := compose.ServeInProcess(p)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	t.Cleanup(closer)
	ctx := context.Background()

	info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	grid, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatalf("grid: %v", err)
	}
	var file *gridwellv1.Tile
	for _, tl := range grid.Tiles {
		if tl.Kind == "text" && tl.AltText == "hello.txt" {
			file = tl
		}
	}
	if file == nil {
		t.Fatalf("no text tile for hello.txt in %v", grid.Tiles)
	}

	// ReadContent streams the metadata body with version 0 (not version-edited).
	stream, err := client.ReadContent(ctx, &gridwellv1.ReadContentRequest{TileId: file.Id})
	if err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	var data []byte
	var version int64
	first := true
	for {
		ch, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if first {
			version = ch.Version
			// hello.txt is PLAIN TEXT now (decision 2026-08-13): its body
			// is the file's own bytes, presented verbatim.
			if ch.MediaType != "text/plain" {
				t.Errorf("media = %q, want text/plain", ch.MediaType)
			}
			first = false
		}
		data = append(data, ch.Data...)
	}
	if string(data) != "hi" {
		t.Errorf("descent body = %q, want the file's own bytes (plain text shows verbatim)", data)
	}
	if version != 0 {
		t.Errorf("fs bodies are not version-edited; version = %d, want 0", version)
	}

	// PlaceTile persists the footprint in the plugin DB.
	placed, err := client.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: file.Id, GridId: file.GridId, X: 5, Y: 6, W: 2, H: 3,
	})
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	if placed.Tile.X != 5 || placed.Tile.Y != 6 || placed.Tile.W != 2 || placed.Tile.H != 3 {
		t.Errorf("placed (%d,%d %dx%d), want (5,6 2x3)",
			placed.Tile.X, placed.Tile.Y, placed.Tile.W, placed.Tile.H)
	}
	// Cross-grid placement is refused (it would be an on-disk move).
	if _, err := client.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: file.Id, GridId: "999999", X: 0, Y: 0, W: 1, H: 1,
	}); err == nil {
		t.Error("cross-grid placement must be refused for fs")
	}
}

// Issue #236: a RENDERABLE file's descent body is the file's real bytes —
// the markdown renderer shows the document itself — while a non-renderable
// file keeps the metadata summary. markdown.Renderable is the one rule.
func TestFSRenderableFileBodyIsTheFile(t *testing.T) {
	dir := t.TempDir()
	doc := []byte("# Notes\n\nreal body\n")
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), doc, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := fs.Open(":memory:", nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	p.SetRoot(dir)

	client, closer, err := compose.ServeInProcess(p)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	t.Cleanup(closer)
	ctx := context.Background()
	info, _ := client.Info(ctx, &gridwellv1.InfoRequest{})
	grid, _ := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	byName := map[string]*gridwellv1.Tile{}
	for _, tl := range grid.Tiles {
		byName[tl.AltText] = tl
	}

	read := func(id string) string {
		stream, err := client.ReadContent(ctx, &gridwellv1.ReadContentRequest{TileId: id})
		if err != nil {
			t.Fatalf("ReadContent: %v", err)
		}
		var out []byte
		for {
			ch, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("recv: %v", err)
			}
			out = append(out, ch.Data...)
		}
		return string(out)
	}

	if got := read(byName["notes.md"].Id); got != string(doc) {
		t.Errorf("renderable body = %q, want the file's own bytes", got)
	}
	if got := read(byName["blob.bin"].Id); got == "\x00\x01\x02" || got == "" {
		t.Errorf("non-renderable body = %q, want the metadata summary", got)
	}
}
