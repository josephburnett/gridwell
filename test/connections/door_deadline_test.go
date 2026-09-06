//go:build connections

// The connection door's deadline rule at the real-binary seam: a Subscribe
// held through the real ssh tunnel for longer than any deadline
// ConnectionDoorServer declares, then proven live by a remote edit's event.
//
// THE RULE: a declared timeout is a fact, and its test is a wait bound to its
// value; a timeout with no such test is untested. The in-package
// internal/server/door_deadline_test.go holds a stream through the door shape;
// this holds one through the PRODUCTION binaries and a real sshd, the level
// where a future Go change to the ssh or h2 path lands first (1b7f98d8: Go
// 1.26.6 armed ReadHeaderTimeout on the raw conn before the unencrypted HTTP/2
// handoff, so a door deadline became a ten-second close on every stream).
//
// The observable symptom here is not the client's own stream ending — the
// local node's fanInRemote retries every five seconds, so the mounter's
// Subscribe stays open across a drop and only flaps. The symptom is the flap
// itself: when the connection door cuts the fan-in's tunneled stream at the
// deadline, fanInRemote publishes an EventPluginHealth Healthy=false for the
// connection (connection.go). So the hold watches for a health-down event and
// for a remote edit's TileChanged that must still arrive on the same stream.
//
// The hold is DERIVED from server.ConnectionDoorServer, the very shape the
// production node puts in front of the door, so it AUTO-TRACKS a re-added
// deadline: the door declares none today, so the hold is the one-second floor
// and the test runs fast; re-add a ten-second deadline and the hold becomes
// 10.5s, long enough for the door to cut the fan-in stream mid-hold and for
// this test to see the health-down and fail with the production symptom.
//
// test/connections may import internal/server: its go.mod replaces the root
// module to ../.. and it already reaches internal (internal/connection/dial/
// dialtest), and test/boundary does not police this leaf module. So we read
// the deadlines off the shape rather than hard-coding a wall-clock constant.

package connections_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gwrpc "github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/connection/dial/dialtest"
	"github.com/josephburnett/gridwell/internal/server"
)

func TestConnectionDoorHoldsATunneledStreamPastAnyDeadline(t *testing.T) {
	// Derived from the production door shape, so a re-added deadline lengthens
	// the hold to catch itself. NotFoundHandler is a stand-in; only the
	// declared deadlines are read.
	shape := server.ConnectionDoorServer(http.NotFoundHandler())
	hold := max(shape.ReadHeaderTimeout, shape.ReadTimeout, shape.WriteTimeout) + 500*time.Millisecond
	if hold < time.Second {
		hold = time.Second
	}

	root := repoRoot(t)
	bin := filepath.Join(root, "gridwell")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("gridwell binary not built (run `make build`): %v", err)
	}

	// Remote node behind a real sshd; local node mounts it through a declared
	// connection, exactly as TestConnectionSpawn stands up the tunnel.
	remoteHome := t.TempDir()
	freshHome(t, remoteHome)
	remoteOrigin, remoteAddr := startServe(t, bin, remoteHome, "127.0.0.1:0")
	creds := dialtest.Server(t, t.TempDir())
	localHome := t.TempDir()
	freshHome(t, localHome)
	appendConnectionsYAML(t, localHome, sshConnectionYAML(t, "holdconn1", creds, remoteAddr))
	localOrigin, _ := startServe(t, bin, localHome, "127.0.0.1:0")

	// The connection gains its root: the tunnel answered and the fan-in is
	// following the remote node's events through the connection door.
	sshRoot := awaitConnRoot(t, localOrigin, "holdconn1")
	ng := rpc(t, localOrigin, "GetGrid", map[string]any{"gridId": sshRoot})
	nodeNS, _ := ng["grid"].(map[string]any)["nodeNs"].(string)
	if nodeNS == "" {
		t.Fatal("the landing grid must carry its serving node's namespace (node_ns)")
	}
	menu := rpc(t, localOrigin, "Handshake", map[string]any{"namespace": nodeNS})
	var remoteHomeRoot string
	for _, p := range menu["plugins"].([]any) {
		if pm := p.(map[string]any); pm["label"] == "home" {
			remoteHomeRoot, _ = pm["rootGridId"].(string)
		}
	}
	if remoteHomeRoot == "" {
		t.Fatalf("the routed menu lacks the remote home: %v", menu["plugins"])
	}

	// A text tile on the remote home, created through the mount, that a
	// foreign writer edits directly on the remote node to drive events across
	// the tunnel.
	num := func(v any) int64 { f, _ := v.(float64); return int64(f) }
	txt := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": remoteHomeRoot,
		"tile":   map[string]any{"kind": "text", "x": 0, "y": 0, "w": 1, "h": 1},
	})["tile"].(map[string]any)
	txtID := txt["id"].(string)
	version := num(txt["version"])
	// Peel the chained id twice to the remote-direct id the far node knows.
	peel := func(id string) string { return strings.SplitN(id, "/", 2)[1] }
	remoteTxtID := peel(peel(txtID))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	events := make(chan gwrpc.Event, 128)
	ended := make(chan error, 1)
	go func() {
		sub, err := clientFor(localOrigin).Subscribe(ctx)
		if err != nil {
			ended <- err
			return
		}
		defer sub.Close()
		for {
			ev, ok, err := sub.Recv()
			if err != nil || !ok {
				ended <- err
				return
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()

	// editRemote makes one real edit directly on the remote node (another
	// device, not this mount) and bumps the tracked version.
	editRemote := func(body string) {
		wt, err := clientFor(remoteOrigin).WriteContent(context.Background(), remoteTxtID, version, []byte(body))
		if err != nil {
			t.Fatalf("remote WriteContent: %v", err)
		}
		version = wt.Version
	}

	// awaitTileEvent drains until a TileChanged for our tile arrives on the
	// still-open stream, editing remotely to drive one, or fails on drop.
	awaitTileEvent := func(what string, editEvery time.Duration) {
		tick := time.NewTicker(editEvery)
		defer tick.Stop()
		deadline := time.After(30 * time.Second)
		n := 0
		editRemote(fmt.Sprintf("# %s edit %d", what, n))
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					t.Fatalf("%s: stream channel closed", what)
				}
				if ev.Kind == gwrpc.EventTileChanged && ev.TileChanged != nil && ev.TileChanged.Tile.ID == txtID {
					return
				}
			case err := <-ended:
				t.Fatalf("%s: the mounter's stream ended: %v", what, err)
			case <-tick.C:
				n++
				editRemote(fmt.Sprintf("# %s edit %d", what, n))
			case <-deadline:
				t.Fatalf("%s: no TileChanged for %s crossed the tunnel", what, txtID)
			}
		}
	}

	// Establish: an event crosses the tunnel and lands on the client stream.
	// The local fan-in dials the remote asynchronously, so the first edits can
	// race stream establishment; edit until one arrives.
	awaitTileEvent("establish", 500*time.Millisecond)

	// HOLD the stream past every deadline the door declares. A connection-door
	// deadline would cut the fan-in's tunneled stream mid-hold; fanInRemote
	// would publish a health-down for the connection before retrying five
	// seconds later. On a door with no deadline the connection stays up and
	// nothing arrives.
	start := time.Now()
	holdDone := time.After(hold)
holdLoop:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("the mounter's stream channel closed during the %s hold", hold)
			}
			if ev.Kind == gwrpc.EventPluginHealth && ev.PluginHealth != nil && !ev.PluginHealth.Healthy {
				t.Fatalf("the connection door cut the fan-in stream %s into the hold: health-down for %q: %s",
					time.Since(start).Round(100*time.Millisecond), ev.PluginHealth.PluginUUID, ev.PluginHealth.Detail)
			}
		case err := <-ended:
			t.Fatalf("the mounter's stream ended %s into the hold: %v", time.Since(start).Round(100*time.Millisecond), err)
		case <-holdDone:
			break holdLoop
		}
	}

	// Proven live: a remote edit after the hold still crosses the tunnel onto
	// the same stream.
	awaitTileEvent("after the hold", time.Second)

	fmt.Printf("connections deadline gate: a tunneled Subscribe held %s past the connection door's deadlines stayed live OK\n", hold)
}
