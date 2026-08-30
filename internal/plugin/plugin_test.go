package plugin_test

import (
	"testing"

	"github.com/josephburnett/gridwell/internal/plugin"
)

// The gridwell.v1 codec round trip — a namespace written onto the wire and
// read back — lives in internal/namespace, the one place a real gRPC loopback
// belongs. Everything here is the registry, which holds Go values; the loader
// is exercised over the real subprocess door in plugin_e2e_test.go.

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
