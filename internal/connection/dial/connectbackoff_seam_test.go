package dial

// The healed-network seam: a far node that is down when the connection is
// dialed and comes up later. Nothing asks gRPC to reconnect; the wait between
// its own attempts is the whole of how fast a mount comes back, and gRPC's
// default grows that wait to two minutes.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/backoff"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/server"
)

// laterDoor prepares the connection door on sock without opening it, so the
// first attempts find no socket at all. The returned func brings it up.
func laterDoor(t *testing.T, sock string) func() {
	t.Helper()
	h := doorHandler(t)
	return func() {
		ln, err := server.ListenConnectionDoor(sock)
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		srv := server.ConnectionDoorServer(h)
		go srv.Serve(ln)
		t.Cleanup(func() { srv.Close() })
	}
}

// answers reports whether the connection is carrying RPCs again. A failing
// RPC is the client's only nudge and it costs nothing: the channel is in
// TRANSIENT_FAILURE and fails fast, so what this loop measures is the
// reconnect the backoff schedules, not the polling.
func answers(t *testing.T, client namespace.Namespace) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := client.Info(ctx, &pb.InfoRequest{})
	return err == nil
}

func TestAConnectionReconnectsWithinTheConnectBackoff(t *testing.T) {
	was := connectBackoff
	t.Cleanup(func() { connectBackoff = was })
	connectBackoff = backoff.Config{BaseDelay: 20 * time.Millisecond, Multiplier: 1.6, Jitter: 0.2, MaxDelay: 50 * time.Millisecond}
	bound := 10 * connectBackoff.MaxDelay

	sock := filepath.Join(t.TempDir(), "federation.sock")
	open := laterDoor(t, sock)
	client, closer, err := Dial(Config{Addr: sock})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(closer)

	// Long enough that a backoff growing from a one-second base has already
	// passed the bound: it is the failed attempts that grow it.
	down := time.Now().Add(3 * time.Second)
	for time.Now().Before(down) {
		if answers(t, client) {
			t.Fatalf("Info answered with no door on %s", sock)
		}
		time.Sleep(10 * time.Millisecond)
	}

	open()
	up := time.Now()
	for !answers(t, client) {
		if time.Since(up) > bound {
			t.Fatalf("no reconnect %v after the door came up; the connection's "+
				"reconnect wait is not connectBackoff", bound)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The default is what a user's mount actually waits out, and the whole point
// of setting it is that gRPC's own default is two minutes.
func TestTheConnectBackoffDefaultCapsWellBelowGRPCs(t *testing.T) {
	if connectBackoff.BaseDelay != time.Second || connectBackoff.MaxDelay != 10*time.Second {
		t.Fatalf("connectBackoff = %+v, want BaseDelay 1s and MaxDelay 10s", connectBackoff)
	}
	if connectBackoff.MaxDelay >= backoff.DefaultConfig.MaxDelay {
		t.Fatalf("connectBackoff.MaxDelay %v does not cap below gRPC's default %v",
			connectBackoff.MaxDelay, backoff.DefaultConfig.MaxDelay)
	}
}
