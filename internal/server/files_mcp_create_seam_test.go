package server_test

// The MCP's create on a directory's grid makes home's row over it and
// nothing on disk.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/josephburnett/gridwell/internal/mcpapi"
)

// A text, url, door or workspace tile created on a directory's grid is a
// Gridwell tile over it, and the directory holds what it held; a file or a
// directory is refused, since the MCP API writes nothing to disk.
func TestMCPCreateOnADirectoryLeavesTheDiskAsItWas(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kept.md"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	n := newFilesNode(t, root)
	cl := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0"}, nil)
	s, err := cl.Connect(n.ctx, &mcp.StreamableClientTransport{Endpoint: n.url + mcpapi.Path,
		HTTPClient: n.web, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	res, err := s.CallTool(n.ctx, &mcp.CallToolParams{Name: "create", Arguments: map[string]any{"items": []map[string]any{
		{"kind": "door", "grid": n.roots[0], "name": "plans"},
		{"kind": "text", "grid": n.roots[0], "body": "# note"},
		{"kind": "url", "grid": n.roots[0], "address": "example.com"},
		{"kind": "workspace", "grid": n.roots[0]},
		{"kind": "file", "grid": n.roots[0], "name": "a.md"},
		{"kind": "directory", "grid": n.roots[0], "name": "sub"},
	}}})
	if err != nil || res.IsError {
		t.Fatalf("create: %v %+v", err, res)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out mcpapi.WriteOut
	if err := json.Unmarshal(b, &out); err != nil || len(out.Items) != 6 {
		t.Fatalf("create answered %s (%v), want six verdicts", b, err)
	}
	for i, v := range out.Items[:4] {
		if v.Verdict != "ok" || v.Tile == nil || v.Tile.Grid != n.roots[0] || !strings.HasPrefix(v.Tile.ID, localNodeID+"/") || strings.Contains(v.Tile.ID, "/~") {
			t.Fatalf("item %d = %+v, want a home row on the directory's grid", i, v)
		}
	}
	if door := out.Items[0].Tile; door.ChildGrid == "" || strings.Contains(door.ChildGrid, "/~") {
		t.Fatalf("the door = %+v, want it holding a Gridwell grid", door)
	}
	for i, v := range out.Items[4:] {
		if !strings.HasPrefix(v.Verdict, "refused:") || !strings.Contains(v.Verdict, "writes nothing to disk") {
			t.Errorf("item %d = %q, want refused, nothing written to disk", i+4, v.Verdict)
		}
	}
	es, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"kept.md"}) {
		t.Fatalf("the directory holds %v, want only kept.md", names)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "kept.md")); string(b) != "kept" {
		t.Fatalf("kept.md holds %q, want it untouched", b)
	}
}
