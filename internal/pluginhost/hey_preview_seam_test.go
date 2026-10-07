package pluginhost_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/plugintest/heyfake"
)

// countingConn counts the unary calls that reach the plugin process, by
// method.
type countingConn struct {
	grpc.ClientConnInterface
	mu    sync.Mutex
	calls map[string]int
}

func (c *countingConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	c.mu.Lock()
	c.calls[method]++
	c.mu.Unlock()
	return c.ClientConnInterface.Invoke(ctx, method, args, reply, opts...)
}

func (c *countingConn) count(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[method]
}

// heyPID is the one hey plugin subprocess this test spawned, 0 while there is
// not exactly one.
func heyPID() int {
	out, _ := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid()), "-f", "gridwell-plugin-hey").Output()
	f := strings.Fields(string(out))
	if len(f) != 1 {
		return 0
	}
	pid, _ := strconv.Atoi(f[0])
	return pid
}

// The trace of 2026-10-07: every hey thread shown asked the plugin for a
// preview it had already said it does not implement. The shipped hey binary
// is asked GetPreview once for a grid of threads, and once more only after a
// respawn, because that is a new process.
func TestHeyIsAskedForPreviewsOncePerProcess(t *testing.T) {
	const uuid = "pheyprv"
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	at := time.Date(2026, 1, 5, 14, 0, 0, 0, time.UTC)
	hey := heyfake.New(t)
	hey.SetBox("imbox",
		heyfake.Thread{TopicID: 101, Subject: "Lunch plans", From: "Alice", Email: "alice@example.com", Created: at},
		heyfake.Thread{TopicID: 102, Subject: "Invoice 41", From: "Bob", Email: "bob@example.com", Created: at},
		heyfake.Thread{TopicID: 103, Subject: "Conference talk", From: "Carol", Email: "carol@example.com", Created: at})
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sup, err := plugin.Supervise(uuid, "hey", plugintest.Binary(t, "hey"),
		hey.Config(map[string]string{"uuid": uuid, "kind": "hey", "state_dir": t.TempDir()}))
	if err != nil {
		t.Fatalf("supervise: %v", err)
	}
	t.Cleanup(sup.Close)
	conn := &countingConn{ClientConnInterface: sup, calls: map[string]int{}}
	a := pluginhost.New(pluginv1.NewPluginClient(conn), st.Namespace(uuid), sup)
	ctx := t.Context()

	info, err := a.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var imbox string
	for _, m := range info.MenuEntries {
		if m.Label == "imbox" {
			imbox = m.GridId
		}
	}
	g, err := a.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: imbox})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) < 3 {
		t.Fatalf("imbox = %v, want the three threads", g.Tiles)
	}
	const method = pluginv1.Plugin_GetPreview_FullMethodName
	preview := func(id string) {
		t.Helper()
		_, err := a.GetTilePreview(ctx, &gridwellv1.GetTilePreviewRequest{TileId: id})
		if status.Code(err) != codes.Unimplemented {
			t.Fatalf("preview of %s = %v, want hey's Unimplemented", id, err)
		}
	}
	for _, tl := range g.Tiles {
		preview(tl.Id)
	}
	if got := conn.count(method); got != 1 {
		t.Fatalf("hey asked GetPreview %d times for %d threads, want once", got, len(g.Tiles))
	}

	pid := heyPID()
	if pid == 0 {
		t.Fatal("no hey plugin subprocess to kill")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", pid, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if up, _ := sup.Health(); up && heyPID() != 0 && heyPID() != pid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the hey plugin never came back")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, tl := range g.Tiles {
		preview(tl.Id)
	}
	if got := conn.count(method); got != 2 {
		t.Fatalf("hey asked GetPreview %d times across two processes, want twice", got)
	}
}
