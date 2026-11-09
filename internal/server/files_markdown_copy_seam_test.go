package server_test

// Markdown crosses the files boundary by copy (CLAUDE.md, 2026-11-08): a text
// tile copied or created in a directory is a new markdown file, a markdown
// file copied out is a text tile holding its body, every other file copies
// out as a link, and a directory refuses anything else.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/mcpapi"
)

// homeText makes a text tile on home holding body.
func (n *filesNode) homeText(body string, x, y int64) *gridwellv1.Tile {
	n.t.Helper()
	made, err := n.cl.CreateWithContent(n.ctx, &gridwellv1.CreateTileRequest{GridId: n.home,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: x, Y: y, W: 2, H: 1}}, []byte(body))
	if err != nil {
		n.t.Fatal(err)
	}
	return made
}

// body reads id's bytes, fatal on a refusal.
func (n *filesNode) body(id string) string {
	n.t.Helper()
	data, _, _, err := n.cl.ReadContent(n.ctx, id)
	if err != nil {
		n.t.Fatalf("ReadContent(%s): %v", id, err)
	}
	return string(data)
}

// names lists dir's names, so a test sees that nothing was written.
func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// refusedAs fails unless err is a verdict (clientsync.OutcomeRejected) saying why.
func refusedAs(t *testing.T, err error, why string) {
	t.Helper()
	if err == nil || clientsync.Of(err) != clientsync.OutcomeRejected || !strings.Contains(err.Error(), why) {
		t.Fatalf("err = %v, want a verdict saying %q", err, why)
	}
}

// A text tile right-dragged into a directory is a new markdown file named
// for the tile, holding its body, at the drop's cell, and the tile it was
// copied from is as it was.
func TestATextTileCopiedIntoADirectoryIsAMarkdownFile(t *testing.T) {
	root := t.TempDir()
	n := newFilesNode(t, root)
	src := n.homeText("# Plan: today/tomorrow\n\nsteps\n", 1, 1)
	made, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: src.Id, DestGridId: n.roots[0], X: 4, Y: 5})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "Plan: todaytomorrow.md")
	if got, err := os.ReadFile(path); err != nil || string(got) != "# Plan: today/tomorrow\n\nsteps\n" {
		t.Fatalf("%s = %q, %v; want the body", path, got, err)
	}
	if made.Id != fileID(path) || made.Kind != rpc.KindText || made.LinkTargetId != "" || made.ContentStamp == "" {
		t.Errorf("copy = %v, want the file's text tile with its stamp", made)
	}
	if got := n.tile(n.roots[0], path); got.X != 4 || got.Y != 5 || got.W != 2 || got.H != 1 {
		t.Errorf("the file lists at %d,%d %dx%d, want the drop's cell 4,5 and the tile's 2x1", got.X, got.Y, got.W, got.H)
	}
	if n.body(src.Id) != "# Plan: today/tomorrow\n\nsteps\n" {
		t.Error("the source's body changed")
	}
	if after, err := n.cl.GetTile(n.ctx, src.Id); err != nil || after.Version != src.Version || after.GridId != n.home {
		t.Errorf("the source = %v, %v; want it untouched", after, err)
	}
}

// A name already in the directory is refused and nothing is written; a
// markdown file copied into another directory is a new file there.
func TestACopyIntoADirectoryReplacesNothing(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	writeFile(t, filepath.Join(root, "Plan.md"), "old")
	writeFile(t, filepath.Join(sub, "keep.txt"), "k")
	n := newFilesNode(t, root)
	src := n.homeText("Plan\n", 1, 1)
	_, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: src.Id, DestGridId: n.roots[0], X: 6, Y: 6})
	refusedAs(t, err, "already")
	if got, _ := os.ReadFile(filepath.Join(root, "Plan.md")); string(got) != "old" {
		t.Errorf("Plan.md = %q, want it as it was", got)
	}
	if got := names(t, root); !slices.Equal(got, []string{"Plan.md", "sub"}) {
		t.Errorf("the root holds %v, want nothing new", got)
	}
	file := n.tile(n.roots[0], filepath.Join(root, "Plan.md"))
	if _, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: file.Id, DestGridId: fileID(sub), X: 3, Y: 0}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(sub, "Plan.md")); string(got) != "old" {
		t.Errorf("sub/Plan.md = %q, want the file's bytes", got)
	}
	_, err = n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: file.Id, DestGridId: n.roots[0], X: 3, Y: 3})
	refusedAs(t, err, "already")
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
	if rpc.CopiesAsLink(md) {
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
	if !rpc.CopiesAsLink(code) {
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

// A directory takes a text tile alone: a url, a door, a link, a shell or a
// workspace tile copied in, a file that is not markdown copied from another
// directory, or a link written there, is refused with the reason and
// nothing is written. A selection's copies are each their own verdict.
func TestADirectoryRefusesAllButAText(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	writeFile(t, filepath.Join(sub, "a.txt"), "a")
	n := newFilesNode(t, root)
	mk := func(tl *gridwellv1.Tile, body string) *gridwellv1.Tile {
		t.Helper()
		var made *gridwellv1.Tile
		var err error
		if body != "" {
			made, err = n.cl.CreateWithContent(n.ctx, &gridwellv1.CreateTileRequest{GridId: n.home, Tile: tl}, []byte(body))
		} else {
			made, err = n.cl.CreateTile(n.ctx, &gridwellv1.CreateTileRequest{GridId: n.home, Tile: tl})
		}
		if err != nil {
			t.Fatalf("create %s: %v", tl.Kind, err)
		}
		return made
	}
	text := n.homeText("Kept\n", 0, 0)
	refused := map[string]*gridwellv1.Tile{
		"url":       mk(&gridwellv1.Tile{Kind: rpc.KindURL, X: 2, Y: 0, W: 1, H: 1, UrlString: "https://example.com/"}, ""),
		"door":      mk(&gridwellv1.Tile{Kind: rpc.KindDoor, X: 3, Y: 0, W: 1, H: 1}, ""),
		"link":      mk(&gridwellv1.Tile{Kind: rpc.KindText, X: 4, Y: 0, W: 1, H: 1, LinkTargetId: text.Id}, ""),
		"shell":     mk(&gridwellv1.Tile{Kind: rpc.KindShell, X: 5, Y: 0, W: 1, H: 1}, ""),
		"workspace": mk(&gridwellv1.Tile{Kind: rpc.KindWorkspace, X: 6, Y: 0, W: 1, H: 1}, ""),
		"a .txt":    n.tile(fileID(sub), filepath.Join(sub, "a.txt")),
	}
	for what, tl := range refused {
		why := rpc.IntoDirectory(tl)
		if why == "" {
			t.Errorf("rpc.IntoDirectory takes a %s, so the ghost would show a copy the node refuses", what)
			continue
		}
		_, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: tl.Id, DestGridId: n.roots[0], X: 7, Y: 7})
		refusedAs(t, err, why)
	}
	_, err := n.cl.CreateTile(n.ctx, &gridwellv1.CreateTileRequest{GridId: n.roots[0], Tile: rpc.LinkOf(text, 7, 7)})
	refusedAs(t, err, "takes no link")
	if got := names(t, root); !slices.Equal(got, []string{"sub"}) {
		t.Errorf("the root holds %v after the refusals, want nothing new", got)
	}
	// A selection of the text and the url, dropped as one: two copies, two verdicts.
	_, textErr := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: text.Id, DestGridId: n.roots[0], X: 0, Y: 2})
	_, urlErr := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: refused["url"].Id, DestGridId: n.roots[0], X: 1, Y: 2})
	if textErr != nil || urlErr == nil {
		t.Errorf("the selection's verdicts = %v, %v; want the text copied and the url refused", textErr, urlErr)
	}
	if got := names(t, root); !slices.Equal(got, []string{"Kept.md", "sub"}) {
		t.Errorf("the root holds %v, want Kept.md alone added", got)
	}
}

// A text tile created in a directory is a new markdown file, as a copy is:
// the one rule for whatever makes a text there.
func TestATextCreatedInADirectoryIsAMarkdownFile(t *testing.T) {
	root := t.TempDir()
	n := newFilesNode(t, root)
	made, err := n.cl.CreateWithContent(n.ctx, &gridwellv1.CreateTileRequest{GridId: n.roots[0],
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 2, Y: 1, W: 1, H: 1}}, []byte("Fresh\n"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "Fresh.md")
	if got, _ := os.ReadFile(path); string(got) != "Fresh\n" || made.Id != fileID(path) || made.X != 2 || made.Y != 1 {
		t.Errorf("create = %v with %q on disk, want Fresh.md at 2,1", made, got)
	}
}

// Over a connection both ways: a far markdown file copied into home is a
// text tile with its body, and a home text copied into a far directory is a
// file on the far disk.
func TestMarkdownCopiesAcrossAConnection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "far.md"), "Far\n")
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil, filesAt(t, "rnode1", dir))
	cl := h.localCl
	lp, err := cl.Handshake(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	home := rpc.HomeGrid(lp)
	out, err := cl.CopyTile(t.Context(), &gridwellv1.CopyTileRequest{TileId: farFileID(filepath.Join(dir, "far.md")), DestGridId: home, X: 1, Y: 1})
	if err != nil {
		t.Fatal(err)
	}
	if body, _, _, err := cl.ReadContent(t.Context(), out.Id); out.Kind != rpc.KindText || out.LinkTargetId != "" || string(body) != "Far\n" || err != nil {
		t.Errorf("far.md copied home = %v holding %q (%v), want a text tile with its body", out, body, err)
	}
	src, err := cl.CreateWithContent(t.Context(), &gridwellv1.CreateTileRequest{GridId: home,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 2, Y: 1, W: 1, H: 1}}, []byte("Near\n"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := cl.CopyTile(t.Context(), &gridwellv1.CopyTileRequest{TileId: src.Id, DestGridId: farFileID(dir), X: 3, Y: 0})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "Near.md")); string(got) != "Near\n" || in.Id != farFileID(filepath.Join(dir, "Near.md")) {
		t.Errorf("Near copied far = %v with %q on the far disk, want Near.md", in, got)
	}
}

// The MCP copy tool takes the same path: a text tile copied into a
// directory with no cell named is a file at the first free cell, and a url
// is refused as a tool error with the reason.
func TestMCPCopiesATextIntoADirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.md"), "a")
	n := newFilesNode(t, root)
	text := n.homeText("Agent note\n", 0, 0)
	link, err := n.cl.CreateTile(n.ctx, &gridwellv1.CreateTileRequest{GridId: n.home,
		Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 2, Y: 0, W: 1, H: 1, UrlString: "https://example.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	cl := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0"}, nil)
	s, err := cl.Connect(n.ctx, &mcp.StreamableClientTransport{Endpoint: n.url + mcpapi.Path, HTTPClient: n.web, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	res, err := s.CallTool(n.ctx, &mcp.CallToolParams{Name: "copy", Arguments: map[string]any{"items": []map[string]any{
		{"id": text.Id, "grid": n.roots[0]}, {"id": link.Id, "grid": n.roots[0]}}}})
	if err != nil {
		t.Fatal(err)
	}
	var out mcpapi.WriteOut
	if b, err := json.Marshal(res.StructuredContent); err != nil || json.Unmarshal(b, &out) != nil {
		t.Fatalf("copy answered %v: %v", res.StructuredContent, err)
	}
	if len(out.Items) != 2 || out.Items[0].Verdict != "ok" || !strings.Contains(out.Items[1].Verdict, rpc.IntoDirectory(link)) {
		t.Fatalf("copy verdicts = %+v, want ok then the directory's refusal", out.Items)
	}
	path := filepath.Join(root, "Agent note.md")
	if got, _ := os.ReadFile(path); string(got) != "Agent note\n" {
		t.Errorf("%s = %q, want the body", path, got)
	}
	if got := n.tile(n.roots[0], path); got.X == 0 && got.Y == 0 {
		t.Errorf("the file landed on a.md's cell 0,0, want the first free one")
	}
}
