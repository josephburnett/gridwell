package server_test

// Markdown crosses the files boundary by copy (CLAUDE.md, 2026-11-08): a
// markdown file copied out is a text tile holding its body, and every other
// file copies out as a link.

import (
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// body reads id's bytes, fatal on a refusal.
func (n *filesNode) body(id string) string {
	n.t.Helper()
	data, _, _, err := n.cl.ReadContent(n.ctx, id)
	if err != nil {
		n.t.Fatalf("ReadContent(%s): %v", id, err)
	}
	return string(data)
}

// A markdown file copied into home is a text tile holding the file's bytes,
// with the file's name, never a link; any other file, a page file among
// them, lands as a link to it.
func TestAMarkdownFileCopiesOutAndAnyOtherLinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.MD"), "# Notes\n\nbody\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	n := newFilesNode(t, root)
	md := n.tile(n.roots[0], filepath.Join(root, "notes.MD"))
	if rpc.CopiesAsLink(md, true) {
		t.Error("rpc.CopiesAsLink says a markdown file links, so the ghost would show a link the node never makes")
	}
	made, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: md.Id, DestGridId: n.home, X: 3, Y: 3})
	if err != nil {
		t.Fatal(err)
	}
	if made.Kind != rpc.KindText || made.LinkTargetId != "" || rpc.IsFilesID(made.Id) || made.Named != md.Named {
		t.Fatalf("copy of a markdown file = %v, want a home text tile", made)
	}
	if got := n.body(made.Id); got != "# Notes\n\nbody\n" {
		t.Errorf("the copy holds %q, want the file's bytes", got)
	}
	code := n.tile(n.roots[0], filepath.Join(root, "main.go"))
	if !rpc.CopiesAsLink(code, true) {
		t.Error("rpc.CopiesAsLink says a .go file copies, so the ghost would show a copy the node never makes")
	}
	linked, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: code.Id, DestGridId: n.home, X: 4, Y: 3})
	if err != nil {
		t.Fatal(err)
	}
	if linked.LinkTargetId != code.Id || rpc.IsFilesID(linked.Id) {
		t.Errorf("copy of a .go file = %v, want a home link to %s", linked, code.Id)
	}
}
