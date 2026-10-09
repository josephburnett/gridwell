package plugin_test

import (
	"testing"

	"github.com/josephburnett/gridwell/internal/plugin"
)

// Everything here is the registry, which holds Go values. The gridwell.v1 codec
// round trip lives in internal/namespace, the one place a real gRPC loopback
// belongs, and the loader is crossed over the real subprocess door in
// plugin_e2e_test.go.

func TestRegistry_GetMissing(t *testing.T) {
	reg := plugin.NewRegistry()
	_, ok := reg.Get("nonexistent")
	if ok {
		t.Error("Get of nonexistent plugin should return false")
	}
}

// An unlabelled plugin answers "", so callers fall back to Info or kind.
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

// Close is terminal for the namespaces and must be for every per-plugin fact.
// A label that survived Close would be inherited by a re-Register.
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

// A switch stops its source once however often it is pressed, and a namespace
// with none is refused rather than recorded off.
func TestRegistry_DisableStopsOnceAndOnlyASwitch(t *testing.T) {
	reg := plugin.NewRegistry()
	stops := 0
	reg.Switch("p1", func() { stops++ })
	if err := reg.Disable("home"); err == nil || reg.Disabled("home") {
		t.Fatal("a namespace with no switch was disabled")
	}
	for range 2 {
		if err := reg.Disable("p1"); err != nil {
			t.Fatal(err)
		}
	}
	if stops != 1 || !reg.Disabled("p1") {
		t.Fatalf("stops = %d, disabled = %v; want one stop and p1 off", stops, reg.Disabled("p1"))
	}
	if got := reg.DisabledNow(); len(got) != 1 || got[0] != "p1" {
		t.Fatalf("DisabledNow = %v, want [p1]", got)
	}
}
