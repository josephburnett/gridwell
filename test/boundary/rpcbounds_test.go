package boundary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unboundedOK names the client RPCs allowed to run on an unbounded context,
// by the source line that opens them. Both are long-lived streams with their
// own re-dial loop, so waiting is what they are for.
var unboundedOK = map[string]string{
	"client/wasm/main.go": "sse.Run(context.Background())",
}

// The gate behind the bounded-RPC rule client/inflight owns. It reads the
// shim, where the contexts are spelled and which `make check` compiles without
// executing. A bare context.Background() on a unary call leaves a read holding
// its dedupe claim forever and a write that can never park, so naming the two
// exceptions here makes each one a decision.
func TestClientRPCsAreBounded(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "client", "wasm")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read client/wasm: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		rel := "client/wasm/" + e.Name()
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, "context.Background()") {
				continue
			}
			if allowed, ok := unboundedOK[rel]; ok && strings.Contains(line, allowed) {
				continue
			}
			t.Errorf("%s:%d: an unbounded context on a client RPC: %s\n"+
				"a request the network swallows never returns, so a read holds its dedupe claim "+
				"for the life of the page and a write is never acknowledged. Use inflight.Bounded, "+
				"or the fetch set's Context, or add the call to unboundedOK if it is a stream.",
				rel, i+1, strings.TrimSpace(line))
		}
	}
}
