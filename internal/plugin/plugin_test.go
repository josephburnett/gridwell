package plugin_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// The gridwell.v1 codec round trip — a namespace written onto the wire and
// read back — lives in internal/namespace, the one place a real gRPC loopback
// belongs. Everything here is the loader and the registry, which hold Go
// values.

// TestRegistry_GetMissing verifies that a missing plugin returns (nil, false).
func TestRegistry_GetMissing(t *testing.T) {
	reg := plugin.NewRegistry()
	_, ok := reg.Get("nonexistent")
	if ok {
		t.Error("Get of nonexistent plugin should return false")
	}
}

// TestRegistry_Label round-trips the configured display name and returns ""
// for an unlabelled plugin, so callers fall back to Info or kind.
func TestRegistry_Label(t *testing.T) {
	reg := plugin.NewRegistry()
	reg.SetLabel("p1", "files")
	if got := reg.Label("p1"); got != "files" {
		t.Errorf("Label(p1) = %q, want files", got)
	}
	if got := reg.Label("unset"); got != "" {
		t.Errorf("Label(unset) = %q, want empty", got)
	}
}

// handshakeRefuser is a plugin whose Info refuses: the shape of "I do not have
// the config I need", FailedPrecondition with the reason.
type handshakeRefuser struct {
	pluginv1.UnimplementedPluginServer
}

func (handshakeRefuser) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return nil, status.Error(codes.FailedPrecondition, "token_file not configured")
}

// TestLoadIntoFailsOnARefusedHandshake: a plugin that cannot answer Info stops
// the launch with its reason, instead of coming up as an empty grid with a log
// line nobody reads.
func TestLoadIntoFailsOnARefusedHandshake(t *testing.T) {
	cfg := &config.ServerConfig{Plugins: []config.PluginConfig{{
		ID: "gl1234a", Label: "todos", Kind: "gitlab", Config: map[string]string{"db_file": filepath.Join(t.TempDir(), "mem.db")},
	}}}
	factories := map[string]plugin.Factory{"gitlab": func(map[string]string) (pluginv1.PluginServer, error) { return handshakeRefuser{}, nil }}
	err := plugin.LoadInto(plugin.NewRegistry(), cfg, factories, testStore(t), nil)
	if err == nil || !strings.Contains(err.Error(), "token_file not configured") || !strings.Contains(err.Error(), "gl1234a") {
		t.Fatalf("LoadInto = %v, want the plugin's own reason, naming it", err)
	}
}

// TestLoadIntoFailsOnARefusingFactory is the in-process door's twin of the
// refused handshake: a Factory, a plugin's FromConfig, that refuses its config
// stops the launch with the reason, naming the plugin. A bundled binary hands
// the loader the same FromConfig the subprocess main hands guest.Main, so a
// bad config cannot come up as something plausible through either door.
func TestLoadIntoFailsOnARefusingFactory(t *testing.T) {
	cfg := &config.ServerConfig{Plugins: []config.PluginConfig{{
		ID: "pr1234a", Label: "procs", Kind: "proc", Config: map[string]string{"pid": "abc", "db_file": filepath.Join(t.TempDir(), "mem.db")},
	}}}
	factories := map[string]plugin.Factory{"proc": func(cfg map[string]string) (pluginv1.PluginServer, error) {
		return nil, fmt.Errorf("pid %q is not a positive process id", cfg["pid"])
	}}
	err := plugin.LoadInto(plugin.NewRegistry(), cfg, factories, testStore(t), nil)
	if err == nil || !strings.Contains(err.Error(), `pid "abc"`) || !strings.Contains(err.Error(), "pr1234a") {
		t.Fatalf("LoadInto = %v, want the factory's reason, naming the plugin", err)
	}
}

// Close is terminal for the namespaces, and must be for every per-plugin
// fact: a label that survives Close would be inherited by a re-Register.
func TestRegistry_CloseForgetsEveryFact(t *testing.T) {
	reg := plugin.NewRegistry()
	reg.Register("p1", "fs", nil, nil)
	reg.SetLabel("p1", "files")
	reg.Close()
	if reg.Label("p1") != "" {
		t.Fatalf("after Close: label=%q, want nothing remembered", reg.Label("p1"))
	}
	if _, ok := reg.Get("p1"); ok {
		t.Fatal("after Close: the namespace is still registered")
	}
	if len(reg.Ordered()) != 0 {
		t.Fatalf("after Close: Ordered() = %v, want empty", reg.Ordered())
	}
}

// testStore is a throwaway node database for the loader.
func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
