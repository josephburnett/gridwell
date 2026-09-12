//go:build connections

// The connection door's deadline rule at the real-binary seam: a Subscribe
// held through a real ssh tunnel for longer than any deadline
// ConnectionDoorServer declares, then proven live by a remote edit's event.
// internal/server/door_deadline_test.go holds a stream through the door shape;
// this holds one through the production binaries and a real sshd, where a Go
// change to the ssh or h2 path lands first.
//
// The symptom is a flap rather than the client's stream ending, because the
// local node's fanInRemote re-dials with backoff and publishes an
// EventPluginHealth with Healthy false when the door cuts its tunneled stream.
// The hold is derived from server.ConnectionDoorServer, so it tracks a
// re-added deadline on its own; the door declares none today, so it is the
// one-second floor.

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

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/connection/dial/dialtest"
	"github.com/josephburnett/gridwell/internal/server"
)

func TestConnectionDoorHoldsATunneledStreamPastAnyDeadline(t *testing.T) {
	// Derived from the production door shape, so a re-added deadline
	// lengthens the hold to catch itself. Only the deadlines are read, so
	// NotFoundHandler stands in for the handler.
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

	// A remote node behind a real sshd, mounted through a declared
	// connection, as TestConnectionSpawn stands up the tunnel.
	remoteHome := t.TempDir()
	freshHome(t, remoteHome)
	remoteOrigin, remoteAddr := startServe(t, bin, remoteHome, "127.0.0.1:0")
	creds := dialtest.Server(t, t.TempDir())
	localHome := t.TempDir()
	freshHome(t, localHome)
	appendConnectionsYAML(t, localHome, sshConnectionYAML(t, "holdconn1", creds, remoteAddr))
	localOrigin, _ := startServe(t, bin, localHome, "127.0.0.1:0")

	// The connection gaining its root means the tunnel answered and the
	// fan-in is following the remote node's events.
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

	// A text tile on the remote home that a foreign writer edits directly
	// on the remote node, to drive events across the tunnel.
	num := func(v any) int64 { f, _ := v.(float64); return int64(f) }
	txt := rpc(t, localOrigin, "CreateTile", map[string]any{
		"gridId": remoteHomeRoot,
		"tile":   map[string]any{"kind": "text", "x": 0, "y": 0, "w": 1, "h": 1},
	})["tile"].(map[string]any)
	txtID := txt["id"].(string)
	version := num(txt["version"])
	// Two peels give the remote-direct id the far node knows.
	peel := func(id string) string { return strings.SplitN(id, "/", 2)[1] }
	remoteTxtID := peel(peel(txtID))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	events := make(chan *gridwellv1.Event, 128)
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

	// editRemote edits directly on the remote node, as another device
	// would, and bumps the tracked version.
	editRemote := func(body string) {
		wt, err := clientFor(remoteOrigin).WriteContent(context.Background(), remoteTxtID, version, []byte(body))
		if err != nil {
			t.Fatalf("remote WriteContent: %v", err)
		}
		version = wt.Version
	}

	// awaitTileEvent drains until a TileChanged for the tile arrives on the
	// still-open stream, editing remotely to drive one.
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
				if c := ev.GetTileChanged(); c != nil && c.GetTile().GetId() == txtID {
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

	// The local fan-in dials the remote asynchronously, so the first edits
	// can race stream establishment. Edit until one arrives.
	awaitTileEvent("establish", 500*time.Millisecond)

	// Hold the stream past every deadline the door declares. A deadline
	// would cut the fan-in's tunneled stream mid-hold and fanInRemote would
	// publish a health-down before retrying. With no deadline the connection
	// stays up and nothing arrives.
	start := time.Now()
	holdDone := time.After(hold)
holdLoop:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("the mounter's stream channel closed during the %s hold", hold)
			}
			if h := ev.GetPluginHealth(); h != nil && !h.Healthy {
				t.Fatalf("the connection door cut the fan-in stream %s into the hold: health-down for %q: %s",
					time.Since(start).Round(100*time.Millisecond), h.PluginUuid, h.Detail)
			}
		case err := <-ended:
			t.Fatalf("the mounter's stream ended %s into the hold: %v", time.Since(start).Round(100*time.Millisecond), err)
		case <-holdDone:
			break holdLoop
		}
	}

	// A remote edit after the hold still crosses the tunnel onto the same
	// stream.
	awaitTileEvent("after the hold", time.Second)

	fmt.Printf("connections deadline gate: a tunneled Subscribe held %s past the connection door's deadlines stayed live OK\n", hold)
}
