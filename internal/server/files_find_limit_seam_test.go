package server_test

// Find's limit caps each answering source (FindRequest.limit), a
// directory's grid answering for the files and for home's rows over it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/mcpapi"
)

// A directory with more matching files than the limit still answers the
// Gridwell text over it, through the router and through the MCP's find.
func TestFindOnADirectoryCapsTheFilesAndHomeEachOnItsOwn(t *testing.T) {
	root := t.TempDir()
	for i := range rpc.FindDefaultLimit + 5 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("n%02d.md", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	n := newFilesNode(t, root)
	note := n.overText(n.roots[0], 40, 40, "Over")
	check := func(via string, ids []string) {
		t.Helper()
		if !slices.Contains(ids, note.Id) {
			t.Errorf("%s: the Gridwell text over the directory is missing from %d results", via, len(ids))
		}
		if want := rpc.FindDefaultLimit + 1; len(ids) != want {
			t.Errorf("%s: %d results, want %d: the files' cap and home's one", via, len(ids), want)
		}
	}

	resp, err := n.raw.Find(n.ctx, connect.NewRequest(&gridwellv1.FindRequest{GridId: n.roots[0], Kind: rpc.KindText}))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range resp.Msg.Results {
		ids = append(ids, r.Tile.GetId())
	}
	check("the router", ids)

	cl := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0"}, nil)
	s, err := cl.Connect(n.ctx, &mcp.StreamableClientTransport{Endpoint: n.url + mcpapi.Path,
		HTTPClient: n.web, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	res, err := s.CallTool(n.ctx, &mcp.CallToolParams{Name: "find",
		Arguments: map[string]any{"grid": n.roots[0], "kind": rpc.KindText}})
	if err != nil || res.IsError {
		t.Fatalf("find: %v %+v", err, res)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var found mcpapi.FindOut
	if err := json.Unmarshal(b, &found); err != nil {
		t.Fatal(err)
	}
	ids = nil
	for _, h := range found.Hits {
		ids = append(ids, h.Tile.ID)
	}
	check("the MCP", ids)
}
