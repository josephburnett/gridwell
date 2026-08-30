//go:build federation

// The MID-SESSION PARTITION gate (docs/offline-plan.md phase 1): the gap
// the spawn tests never covered — a mount that dies UNDER a live session.
// Runs the production binaries through a real ssh tunnel, reads through
// the mount to warm the node-side cache (internal/sourcecache), then
// SIGKILLs the remote node and asserts the whole offline story end to end:
// warmed reads serve stale, never-read bytes fail honestly, the offline
// deep copy degrades exactly per the owner decision (cached → copy,
// uncached → link), and a revived remote answers live again — the cache
// never masks a healed mount.

package federation_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/remote/dial/dialtest"
)

func TestMountPartitionServesCache(t *testing.T) {
	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}
	ctx := context.Background()
	num := func(v any) int64 { f, _ := v.(float64); return int64(f) }

	// Remote node: one local. Keep its address — the revival must land on
	// the SAME addr the connection dials.
	remoteHome := t.TempDir()
	freshHome(t, remoteHome)
	// The federation socket lives under the home, so a revival on the same
	// home lands on the SAME path the connection dials.
	remoteOrigin, remoteAddr, stopRemote := startServeProc(t, bin, remoteHome, "127.0.0.1:0")
	creds := dialtest.Server(t, t.TempDir())

	// Local node: localdb + the builtin transport; the connection is
	// server.yaml CONFIG (v2 #269), declared before first serve.
	localHome := t.TempDir()
	freshHome(t, localHome)
	appendConnectionsYAML(t, localHome, sshConnectionYAML(t, "partconn1", creds, remoteAddr))
	localOrigin, _ := startServe(t, bin, localHome, "127.0.0.1:0")
	cl := clientFor(localOrigin)

	// The connection lands on the remote HOME — personal's root grid
	// (remote-menu, 2026-08-16) — writable directly. The transport's id
	// (the cache file's name) is the row's leading segment.
	personalChild := awaitConnRoot(t, localOrigin, "partconn1")
	lp := rpc(t, localOrigin, "Handshake", map[string]any{})
	var homeRoot string
	for _, p := range lp["plugins"].([]any) {
		pm := p.(map[string]any)
		if pm["label"] == "home" {
			homeRoot, _ = pm["rootGridId"].(string)
		}
	}

	// Through the chain: a well holding a WARMED text, a NEVER-READ text.
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

	// WARM the cache: the grids and exactly one body. (Writes are never
	// cached — only these reads are.)
	rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": personalChild})
	rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": wellChild})
	if body, _, _, err := cl.ReadContent(ctx, warmT["id"].(string)); err != nil || string(body) != "warmed words" {
		t.Fatalf("warm read = %q (%v)", body, err)
	}
	if _, err := os.Stat(filepath.Join(localHome, "cache.db")); err != nil {
		t.Fatalf("source cache file missing (the node wiring): %v", err)
	}

	// ── THE PARTITION ──────────────────────────────────────────────────
	stopRemote()

	// Warmed reads serve STALE (poll: the dial layer needs a beat to start
	// answering Unavailable instead of hanging on half-open sockets).
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
	g := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": wellChild})
	if len(g["tiles"].([]any)) != 2 {
		t.Fatalf("dark grid read has %d tiles, want the cached 2", len(g["tiles"].([]any)))
	}
	// The memory says so on the wire (#256): the cache-served grid wears
	// the stale bit through the whole real chain — sourcecache → server →
	// Connect JSON — which is what the client's offline chip reads.
	if gm, ok := g["grid"].(map[string]any); !ok || gm["stale"] != true {
		t.Fatalf("dark grid read grid=%v, want stale=true on the wire", g["grid"])
	}
	// A never-read body fails HONESTLY — served-wrong would be worse than
	// unavailable.
	if _, _, _, err := cl.ReadContent(ctx, coldT["id"].(string)); err == nil {
		t.Fatal("dark read of never-cached bytes must fail, not fabricate")
	}

	// The OFFLINE DEEP COPY (the owner-decision scenario, end to end over
	// real binaries): right-drag the remote well into the local plugin
	// while the mount is dark. Cached text → real copy; never-read text →
	// LINK to the original.
	copyResp := rpc(t, localOrigin, "CloneTile", map[string]any{
		"tileId": well["id"], "version": 0, "destGridId": homeRoot, "x": 5, "y": 5,
	})["tile"].(map[string]any)
	if copyResp["reference"] == true {
		t.Fatal("the offline copy's top well must be SOLID (its grid was cached)")
	}
	cg := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": copyResp["childGridId"]})
	var gotCopy, gotLink map[string]any
	for _, ti := range cg["tiles"].([]any) {
		tm := ti.(map[string]any)
		if lt, _ := tm["linkTargetId"].(string); lt != "" {
			gotLink = tm
		} else if tm["kind"] == "text" {
			gotCopy = tm
		}
	}
	if gotCopy == nil || gotLink == nil {
		t.Fatalf("offline copy shape wrong (want one solid text + one link): %v", cg["tiles"])
	}
	if body, _, _, err := cl.ReadContent(ctx, gotCopy["id"].(string)); err != nil || string(body) != "warmed words" {
		t.Fatalf("offline-copied body = %q (%v)", body, err)
	}
	if gotLink["linkTargetId"] != coldT["id"] {
		t.Fatalf("offline link targets %v, want the original %v", gotLink["linkTargetId"], coldT["id"])
	}

	// ── THE REVIVAL ────────────────────────────────────────────────────
	// Same address, same DB: the connection self-heals (sshdial backoff
	// caps at 10s) and the cold body reads LIVE — proof the cache answers
	// only when the mount cannot.
	if _, _, stop2 := startServeProc(t, bin, remoteHome, strings.TrimPrefix(remoteOrigin, "http://")); stop2 == nil {
		t.Fatal("remote revival failed")
	}
	deadline = time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if body, _, _, rerr := cl.ReadContent(ctx, coldT["id"].(string)); rerr == nil {
			if string(body) != "cold words" {
				t.Fatalf("revived read = %q, want the live bytes", body)
			}
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("the mount never healed after revival on %s", remoteAddr))
}
