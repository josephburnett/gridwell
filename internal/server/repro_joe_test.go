package server

// Throwaway reproduction over a copy of the real home: preexisting pane tile
// misbehaving, GetTile/ReadContent answering NotFound. Not for commit.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/plugintest/gitlabfake"
	"github.com/josephburnett/gridwell/internal/sourcecache"
)

const (
	joeNodeID  = "52f8374fa356402c66e41b8097341b09"
	joeGitlab  = "ngkwanw"
	joeSrcMain = "/tmp/gw-debug/gridwell.db"
	joeSrcCache = "/tmp/gw-debug/cache.db"
)

func cp(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("no captured home at %s", src)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReproJoeHome(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gridwell.db")
	cachePath := filepath.Join(dir, "cache.db")
	cp(t, joeSrcMain, dbPath)
	cp(t, joeSrcCache, cachePath)

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	gl := gitlabfake.New(t)
	glCfg := gl.Config(t, map[string]string{"refresh": "1ns"})
	cpClient := plugintest.Spawn(t, "gitlab", glCfg)

	cache, err := sourcecache.Open(cachePath)
	if err != nil {
		t.Fatalf("cache open: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })

	reg := plugin.NewRegistry()
	reg.Register(joeNodeID, "home", local.New(st, nil), nil)
	reg.Register(joeGitlab, "gitlab", cache.Front(pluginhost.New(cpClient, st.Namespace(joeGitlab)), sourcecache.Options{}), nil)

	srv := mustNew(t, reg, Config{ID: joeNodeID})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx := context.Background()

	report := func(what string, err error) {
		if err != nil {
			fmt.Printf("REPRO %-40s ERR %v\n", what, err)
			return
		}
		fmt.Printf("REPRO %-40s OK\n", what)
	}

	// The console's failing verbs, against preexisting ids.
	_, err = cl.GetTile(ctx, joeNodeID+"/1468")
	report("home GetTile pane 1468", err)
	_, err = cl.GetTile(ctx, joeGitlab+"/8821")
	report("gitlab GetTile todo row 8821", err)
	_, _, _, err = cl.ReadContent(ctx, joeGitlab+"/8821")
	report("gitlab ReadContent todo row 8821", err)
	_, err = cl.GetTile(ctx, joeGitlab+"/1534")
	report("gitlab GetTile week well 1534", err)

	g, err := cl.GetGrid(ctx, joeGitlab+"/75")
	report("gitlab GetGrid root context 75", err)
	if err == nil {
		fmt.Printf("REPRO root grid stale=%v tiles=%d\n", g.Grid.Stale, len(g.Tiles))
	}

	// The trash drag: what the client does is a DeleteTile on the pane tile.
	_, err = cl.GetTilePreview(ctx, joeNodeID+"/1468")
	report("home preview pane 1468", err)
	dresp, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{GridID: joeNodeID + "/1", X: 90, Y: 90, W: 1, H: 1})
	report("home CreateWell sanity", err)
	if err == nil {
		report("home DeleteTile sanity well", cl.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: dresp.ID}))
	}
	report("home DeleteTile pane 1468 (trash park)", cl.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: joeNodeID + "/1468"}))

	time.Sleep(200 * time.Millisecond) // let any revalidation land before teardown
}

func TestReproJoePaneContent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gridwell.db")
	cp(t, joeSrcMain, dbPath)
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	reg.Register(joeNodeID, "home", local.New(st, nil), nil)
	srv := mustNew(t, reg, Config{ID: joeNodeID})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx := context.Background()

	for _, id := range []string{"1468", "1520", "1289", "390", "1464", "757"} {
		data, mt, ver, rerr := cl.ReadContent(ctx, joeNodeID+"/"+id)
		if rerr != nil {
			fmt.Printf("REPRO pane %s ReadContent ERR %v\n", id, rerr)
			continue
		}
		fmt.Printf("REPRO pane %s ReadContent OK %d bytes mt=%q ver=%d\n", id, len(data), mt, ver)
	}
}
