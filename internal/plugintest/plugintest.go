// Package plugintest is the seam harness for a test that needs a plugin.v1
// client.
//
// Spawn is the door for a SHIPPED plugin: a plugin lives in its own
// repository (github.com/josephburnett/gridwell-plugins) and reaches this one
// exactly one way — compose.LoadPlugin spawns gridwell-plugin-<kind> and
// speaks plugin.v1 to it. No gridwell package, test files included, may
// import a plugin implementation; the module boundary makes it impossible and
// test/boundary pins it. So a test that needs a real plugin behind the
// adapter spawns the real binary with the config server.yaml would have given
// it. Binary is the one place that locates a built binary and Spawn the one
// place that launches one; both answer with a t.Fatal naming what to build,
// never a skip, since a skip would leave the seam unexercised while the suite
// stayed green.
//
// Loopback is the door for a plugin the TEST ITSELF declares — a stub that
// answers one way, to pin what the adapter does with the answer. It is a real
// gRPC server on an in-memory listener, not a direct method call, so the
// marshalling the wire does still happens: a message the plugin cannot
// serialize fails here too, and no answer is shared by pointer across the
// seam.
package plugintest

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/josephburnett/gridwell/api/compose"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
)

// Loopback serves impl over an in-memory gRPC connection and returns the
// connected client plus its closer. No socket, so nothing outside the process
// can reach it.
func Loopback(impl pluginv1.PluginServer) (pluginv1.PluginClient, func(), error) {
	lis := bufconn.Listen(1 << 20)

	srv := grpc.NewServer()
	pluginv1.RegisterPluginServer(srv, impl)
	go srv.Serve(lis)

	cc, err := grpc.NewClient("passthrough:///plugintest",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		srv.Stop()
		return nil, nil, fmt.Errorf("plugintest loopback dial: %w", err)
	}

	closer := func() {
		cc.Close()
		srv.GracefulStop()
	}
	return pluginv1.NewPluginClient(cc), closer, nil
}

// repoRoot finds this repository's root by walking up from the test's
// working directory to the go.mod that declares the root module.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			if strings.HasPrefix(string(data), "module github.com/josephburnett/gridwell\n") {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("plugintest: no gridwell go.mod above the test directory")
		}
		dir = parent
	}
}

// Binary resolves gridwell-plugin-<kind> the way the loader does
// (internal/cli.resolveBinary): GRIDWELL_PLUGIN_DIR names the directory, and
// otherwise it is the repository root, which is where `make build` writes the
// binaries it builds out of the plugins repo.
func Binary(t *testing.T, kind string) string {
	t.Helper()
	name := "gridwell-plugin-" + kind
	dir := os.Getenv("GRIDWELL_PLUGIN_DIR")
	if dir == "" {
		dir = repoRoot(t)
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("plugintest: %s not found at %s: run `make plugins` (it builds from PLUGINS_DIR, the gridwell-plugins checkout beside this one) or set GRIDWELL_PLUGIN_DIR", name, path)
	}
	return path
}

// Spawn launches the shipped gridwell-plugin-<kind> with cfg — exactly the
// production spawn, config map and all — and returns the connected client,
// killed at the end of the test.
//
// The guest inherits this process's environment, so the test's own home is
// redirected first: an fs plugin trashes a deleted file into
// $XDG_DATA_HOME/Trash, and a test must never write into the developer's. Its
// state_dir is redirected for the same reason — see withStateDir.
func Spawn(t *testing.T, kind string, cfg map[string]string) pluginv1.PluginClient {
	t.Helper()
	cp, _ := SpawnCloser(t, kind, cfg)
	return cp
}

// SpawnCloser is Spawn with the kill handed back, for a test that must stop
// the plugin mid-test — a restart, a crash. The kill also runs at the end of
// the test, and running it twice is harmless.
func SpawnCloser(t *testing.T, kind string, cfg map[string]string) (pluginv1.PluginClient, func()) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cp, kill, err := compose.LoadPlugin(Binary(t, kind), withStateDir(t, cfg))
	if err != nil {
		t.Fatalf("spawn gridwell-plugin-%s: %v", kind, err)
	}
	t.Cleanup(kill)
	return cp, kill
}

// withStateDir copies cfg with a state_dir, the private directory the loader
// hands a plugin in production (<home>/plugins/<id>): a per-test temp
// directory, so no test writes into a real home. A test that keeps a plugin's
// memory across a restart passes its own directory and this leaves it alone.
// The copy keeps the caller's map its own.
func withStateDir(t *testing.T, cfg map[string]string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(cfg)+1)
	for k, v := range cfg {
		out[k] = v
	}
	if out["state_dir"] == "" {
		out["state_dir"] = t.TempDir()
	}
	return out
}
