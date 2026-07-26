package server

import (
	"context"
	"net/http/httptest"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/shellsvc"
	"github.com/josephburnett/gridwell/internal/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/store"
)

// The shell-capable single-plugin harness. It outlived the WS bridge tests it
// was written for (the bridge died 2026-07-26 — shell bytes ride the Electron
// main process's gRPC OpenShell against the node export now): the session
// door tests still stand their server up through it.

const shellPluginUUID = "shelltest-uuid"

// newShellBridgeServer builds an httptest server whose single localdb plugin
// owns a fake shell streamer. Returns the server, the fake (to assert on the
// PTY decisions), and the qualified root grid id.
func newShellBridgeServer(t *testing.T) (*httptest.Server, *shellsvctest.FakeStreamer, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	fake := shellsvctest.New()
	p := localdb.New(st, shellsvc.NewManager(fake))
	client, closer, err := plugin.ServeInProcess(p)
	if err != nil {
		t.Fatalf("ServeInProcess: %v", err)
	}
	t.Cleanup(closer)

	reg := plugin.NewRegistry()
	reg.Register(shellPluginUUID, "localdb", client, nil)
	srv := New(reg, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	info, err := client.Info(context.Background(), &pb.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	return hs, fake, shellPluginUUID + "/" + info.RootGridId
}
