package providerhost_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/compose"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/convert"
	"github.com/josephburnett/gridwell/internal/layout"
	"github.com/josephburnett/gridwell/internal/parity"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/providerhost"
	"github.com/josephburnett/gridwell/internal/server"
	fsplugin "github.com/josephburnett/gridwell/plugins/fs"
	fsprovider "github.com/josephburnett/gridwell/plugins/fs/provider"
)

// The REAL-DATA migration gate (docs/v2-design.md §8.4): point this at a
// COPY of a production fs plugin DB (a `gridwell backup` snapshot) and it
// converts + crawls both stacks over the live directory, scoped to the
// grids the legacy DB had materialized. Env-gated: machine-specific data,
// run by the operator before cutover, skipped everywhere else.
//
//	GRIDWELL_REALGATE_FS_DB=/path/to/copy/store.db \
//	GRIDWELL_REALGATE_FS_UUID=umyd7dx \
//	GRIDWELL_REALGATE_FS_ROOT=/home/joe \
//	go test -run TestRealDataFSGate ./internal/providerhost/
func TestRealDataFSGate(t *testing.T) {
	legacyDB := os.Getenv("GRIDWELL_REALGATE_FS_DB")
	uuid := os.Getenv("GRIDWELL_REALGATE_FS_UUID")
	root := os.Getenv("GRIDWELL_REALGATE_FS_ROOT")
	if legacyDB == "" || uuid == "" || root == "" {
		t.Skip("real-data gate: set GRIDWELL_REALGATE_FS_{DB,UUID,ROOT} to run")
	}
	memPath := filepath.Join(t.TempDir(), "mem.db")
	res, err := convert.FS(legacyDB, memPath, uuid, "fs", root)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	t.Logf("converted: %d grids, %d tiles", res.Grids, res.Tiles)

	// Legacy node over the copy (its reconcile writes to the copy).
	lp, err := fsplugin.Open(legacyDB, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lp.Close() })
	lp.SetRoot(root)
	lc, lclose, err := plugin.ServeInProcess(lp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lclose)
	lreg := plugin.NewRegistry()
	lreg.Register(uuid, "fs", lc, nil)
	lsrv := httptest.NewServer(server.New(lreg, server.Config{}).Handler())
	t.Cleanup(lsrv.Close)
	legacy := rpc.NewClient(lsrv.Client(), lsrv.URL, connect.WithProtoJSON())

	// v2 stack over the converted memory.
	mem, err := layout.Open(memPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	cp, cpClose, err := compose.ServeProviderInProcess(fsprovider.New(root, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cpClose)
	vc, vclose, err := plugin.ServeInProcess(providerhost.New(cp, mem))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vclose)
	vreg := plugin.NewRegistry()
	vreg.Register(uuid, "fs", vc, nil)
	vsrv := httptest.NewServer(server.New(vreg, server.Config{}).Handler())
	t.Cleanup(vsrv.Close)
	v2 := rpc.NewClient(vsrv.Client(), vsrv.URL, connect.WithProtoJSON())

	allow := map[string]bool{}
	for _, gid := range res.GridIDs {
		allow[uuid+"/"+strconv.FormatInt(gid, 10)] = true
	}
	sa, sb, err := parity.CrawlPair(context.Background(), legacy, v2, parity.Options{GridAllow: allow})
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	t.Logf("crawled %d grids / %d tiles (legacy), %d/%d (v2), %d scoped-out",
		len(sa.Grids), len(sa.Tiles), len(sb.Grids), len(sb.Tiles), len(sa.Skipped))
	if diffs := parity.Diff(sa, sb, parity.Policy{}); len(diffs) != 0 {
		t.Fatalf("REAL-DATA PARITY FAILED (%d):\n%s", len(diffs), strings.Join(diffs, "\n"))
	}
}
