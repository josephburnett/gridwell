package server_test

// The shipped proc plugin's Watch, spawned as the loader spawns it and
// started through pluginhost.Start, reaching a client at the web door: the
// plugin's poll of a shown pid over the real /proc, the node's stream and the
// fan-out.

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// forker is a real process that starts a `sleep` child for each line written
// to it, and nothing else, so its child set changes only when the test says.
type forker struct {
	pid string
	in  io.WriteCloser
}

func startForker(t *testing.T) *forker {
	t.Helper()
	cmd := exec.Command("sh", "-c", "while read x; do sleep 600 & done")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start a forking process: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	return &forker{pid: strconv.Itoa(cmd.Process.Pid), in: in}
}

func (f *forker) fork(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(f.in, "x\n"); err != nil {
		t.Fatal(err)
	}
}

// A child started under the pid a client shows reaches it as that grid's
// change; one started under a pid nobody shows reaches it as nothing.
func TestProcWatchReachesAClientShowingThePid(t *testing.T) {
	const procUUID = "pprocwatch"
	shownProc, unshownProc := startForker(t), startForker(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cp := plugintest.Spawn(t, "proc", map[string]string{"pid": shownProc.pid})
	a, stop := pluginhost.Start(cp, st.Namespace(procUUID), nil, "plugin "+procUUID+" watch")
	reg := plugin.NewRegistry()
	reg.Register(procUUID, "proc", a, stop)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var shown string
	for _, p := range lp.Plugins {
		if p.Uuid == procUUID {
			shown = plugintest.LandingOf(t, p)
		}
	}
	if shown == "" {
		t.Fatal("no proc plugin in the handshake")
	}

	events := make(chan *gridwellv1.Event, 64)
	go func() {
		es, err := cl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer es.Close()
		for {
			ev, ok, err := es.Recv()
			if err != nil || !ok {
				return
			}
			events <- ev
		}
	}()
	if err := cl.SetInterest(ctx, []string{shown}); err != nil {
		t.Fatal(err)
	}
	await := func(what string) {
		t.Helper()
		for {
			select {
			case ev := <-events:
				if ev.GetGridChanged().GetGridId() == shown {
					return
				}
			case <-ctx.Done():
				t.Fatalf("%s: no change to %s reached the client", what, shown)
			}
		}
	}
	// The stream's open is announced once the plugin polls the pid.
	await("the Watch opening")
	quiet := func(what string) {
		t.Helper()
		for {
			select {
			case ev := <-events:
				t.Fatalf("%s: the client was told %v", what, ev)
			case <-time.After(5 * time.Second): // past two of the plugin's 2 s polls
				return
			}
		}
	}
	quiet("after the open")

	unshownProc.fork(t)
	quiet("a child started under a pid no one shows")

	shownProc.fork(t)
	await("a child started under the shown pid")
}
