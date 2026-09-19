//go:build connections

// The mid-session partition gate: a mount that dies under a live session. It
// warms internal/sourcecache through a real tunnel, SIGKILLs the remote node,
// and asserts the offline story end to end: a warmed read serves the memory,
// never-read bytes fail honestly, the offline deep copy copies what is cached
// and links what is not, and a revived remote answers live and re-kicks the
// prefetch walk.

package connections_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/connection/dial/dialtest"
)

// awaitConnHealth reads the client's event stream until one connection's
// health says what is expected, over the path health really takes: fan-in,
// qualification, the source cache's arm, then the client's stream.
func awaitConnHealth(t *testing.T, health <-chan *gridwellv1.Event, conn string, want bool) {
	t.Helper()
	deadline := time.After(90 * time.Second)
	for {
		select {
		case ev := <-health:
			if h := ev.GetPluginHealth(); h != nil && strings.Contains(h.PluginUuid, conn) && h.Healthy == want {
				return
			}
		case <-deadline:
			t.Fatalf("connection %s never reported healthy=%v", conn, want)
		}
	}
}

func TestMountPartitionServesCache(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}
	ctx := context.Background()
	num := func(v any) int64 { f, _ := v.(float64); return int64(f) }

	// The revival must land on the same address the connection dials, so
	// the remote node's address is kept.
	remoteHome := t.TempDir()
	freshHome(t, remoteHome)
	// The connection door's socket lives under the home, so a revival on
	// the same home lands on the same path.
	remoteOrigin, remoteAddr, stopRemote := startServeProc(t, bin, remoteHome, "127.0.0.1:0")
	creds := dialtest.Server(t, t.TempDir())

	// The local node's connection is server.yaml config, declared before
	// first serve.
	localHome := t.TempDir()
	freshHome(t, localHome)
	appendConnectionsYAML(t, localHome, sshConnectionYAML(t, "partconn1", creds, remoteAddr))
	localOrigin, _ := startServe(t, bin, localHome, "127.0.0.1:0")
	cl := clientFor(localOrigin)

	// The connection lands on the remote home, writable directly. The
	// transport's id, which names the cache file, is the row's leading
	// segment.
	personalChild := awaitConnRoot(t, localOrigin, "partconn1")
	lp := rpc(t, localOrigin, "Handshake", map[string]any{})
	var homeRoot string
	for _, p := range lp["plugins"].([]any) {
		pm := p.(map[string]any)
		if pm["label"] == "home" {
			homeRoot, _ = pm["rootGridId"].(string)
		}
	}

	// One live subscription, as the real client holds one: it carries the
	// connection's health through the source cache and triggers the
	// whole-source walk. It opens before the tiles exist, so that walk cannot
	// have warmed anything below, and lives in a goroutine because over
	// HTTP/1.1 the call does not return until the first event arrives.
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	health := make(chan *gridwellv1.Event, 64)
	go func() {
		stream, serr := cl.Subscribe(subCtx)
		if serr != nil {
			return
		}
		defer stream.Close()
		for {
			ev, ok, rerr := stream.Recv()
			if !ok || rerr != nil {
				return
			}
			if ev.GetPluginHealth() != nil {
				select {
				case health <- ev:
				default:
				}
			}
		}
	}()
	time.Sleep(3 * time.Second)

	// Through the chain: a well holding one text that is warmed and one
	// that is never read.
	well := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": personalChild,
		"tile":   map[string]any{"kind": "well", "x": 0, "y": 0, "w": 1, "h": 1, "altText": "trip"},
	})["tile"].(map[string]any)
	wellChild := well["childGridId"].(string)
	warmT := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": wellChild,
		"tile":   map[string]any{"kind": "text", "x": 0, "y": 0, "w": 1, "h": 1},
	})["tile"].(map[string]any)
	if _, err := cl.WriteContent(ctx, warmT["id"].(string), num(warmT["version"]), []byte("warmed words")); err != nil {
		t.Fatal(err)
	}
	coldT := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": wellChild,
		"tile":   map[string]any{"kind": "text", "x": 2, "y": 0, "w": 1, "h": 1},
	})["tile"].(map[string]any)
	if _, err := cl.WriteContent(ctx, coldT["id"].(string), num(coldT["version"]), []byte("cold words")); err != nil {
		t.Fatal(err)
	}
	// A second never-read text for the re-warm after the revival. It is not
	// read even then, so only the walk can warm it.
	colderT := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": wellChild,
		"tile":   map[string]any{"kind": "text", "x": 4, "y": 0, "w": 1, "h": 1},
	})["tile"].(map[string]any)
	if _, err := cl.WriteContent(ctx, colderT["id"].(string), num(colderT["version"]), []byte("colder words")); err != nil {
		t.Fatal(err)
	}

	// Warm the cache with the grids and one body. A write is never cached.
	rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": personalChild})
	rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": wellChild})
	if body, _, _, err := cl.ReadContent(ctx, warmT["id"].(string)); err != nil || string(body) != "warmed words" {
		t.Fatalf("warm read = %q (%v)", body, err)
	}
	if _, err := os.Stat(filepath.Join(localHome, "cache.db")); err != nil {
		t.Fatalf("source cache file missing (the node wiring): %v", err)
	}

	// The partition.
	stopRemote()

	// Warmed reads serve the remembering. This polls, because the dial layer
	// needs a beat to answer Unavailable instead of hanging on half-open
	// sockets.
	deadline := time.Now().Add(60 * time.Second)
	var staleBody []byte
	var err error
	for time.Now().Before(deadline) {
		staleBody, _, _, err = cl.ReadContent(ctx, warmT["id"].(string))
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil || string(staleBody) != "warmed words" {
		t.Fatalf("dark warmed read = %q (%v), want the cached bytes", staleBody, err)
	}
	// The room itself still serves, whole, out of the cache. Nothing on the
	// answer says it is a memory: that is the connection's health, awaited
	// below, and the client's offline chip is drawn from there.
	g := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": wellChild})
	if len(g["tiles"].([]any)) != 3 {
		t.Fatalf("dark grid read has %d tiles, want the cached 3", len(g["tiles"].([]any)))
	}
	// A never-read body fails rather than serving something wrong. colderT
	// failing here also says the establishment walk never reached it, so the
	// re-warm below can only be the recovery's doing.
	if _, _, _, err := cl.ReadContent(ctx, coldT["id"].(string)); err == nil {
		t.Fatal("dark read of never-cached bytes must fail, not fabricate")
	}
	if _, _, _, err := cl.ReadContent(ctx, colderT["id"].(string)); err == nil {
		t.Fatal("the second never-read body was already cached before any recovery")
	}
	awaitConnHealth(t, health, "partconn1", false)

	// The offline deep copy: right-drag the remote well into the local
	// plugin while the mount is dark. Cached text becomes a real copy and
	// never-read text becomes a link to the original.
	copyResp := rpc(t, localOrigin, "CloneTile", map[string]any{
		"tileId": well["id"], "version": 0, "destGridId": homeRoot, "x": 5, "y": 5,
	})["tile"].(map[string]any)
	if copyResp["reference"] == true {
		t.Fatal("the offline copy's top well must be SOLID (its grid was cached)")
	}
	cg := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": copyResp["childGridId"]})
	var gotCopy map[string]any
	linked := map[string]bool{}
	for _, ti := range cg["tiles"].([]any) {
		tm := ti.(map[string]any)
		if lt, _ := tm["linkTargetId"].(string); lt != "" {
			linked[lt] = true
		} else if tm["kind"] == "text" {
			gotCopy = tm
		}
	}
	if gotCopy == nil || len(linked) != 2 {
		t.Fatalf("offline copy shape wrong (want one solid text + two links): %v", cg["tiles"])
	}
	if body, _, _, err := cl.ReadContent(ctx, gotCopy["id"].(string)); err != nil || string(body) != "warmed words" {
		t.Fatalf("offline-copied body = %q (%v)", body, err)
	}
	if !linked[coldT["id"].(string)] || !linked[colderT["id"].(string)] {
		t.Fatalf("offline links target %v, want the two never-read originals", linked)
	}

	// The revival. On the same address and the same DB the connection
	// self-heals, its dial backoff capping at 10s, and the cold body reads
	// live, so the cache answers only when the mount cannot.
	_, _, stop2 := startServeProc(t, bin, remoteHome, strings.TrimPrefix(remoteOrigin, "http://"))
	if stop2 == nil {
		t.Fatal("remote revival failed")
	}
	deadline = time.Now().Add(60 * time.Second)
	healed := false
	for time.Now().Before(deadline) {
		if body, _, _, rerr := cl.ReadContent(ctx, coldT["id"].(string)); rerr == nil {
			if string(body) != "cold words" {
				t.Fatalf("revived read = %q, want the live bytes", body)
			}
			healed = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !healed {
		t.Fatal(fmt.Sprintf("the mount never healed after revival on %s", remoteAddr))
	}

	// A recovered connection re-kicks the source's prefetch walk without a
	// restart. colderT is never read live, so only that walk can warm it, and
	// the health-up on this stream passed through the cache's arm.
	awaitConnHealth(t, health, "partconn1", true)
	time.Sleep(20 * time.Second)
	stop2()
	deadline = time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if body, _, _, rerr := cl.ReadContent(ctx, colderT["id"].(string)); rerr == nil {
			if string(body) != "colder words" {
				t.Fatalf("re-warmed read = %q, want the cached bytes", body)
			}
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("a second partition could not serve the bytes nobody read: the recovery did not re-walk the source")
}
