package server

import (
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
)

func TestStripUUID(t *testing.T) {
	cases := []struct {
		id, uuid, want string
	}{
		{"fs/5", "fs", "5"},          // matching prefix stripped
		{"fs/5", "proc", "fs/5"},     // foreign prefix untouched
		{"5", "fs", "5"},             // bare id untouched
		{"", "fs", ""},               // empty untouched
		{"fs/9/nested", "fs", "9/nested"}, // only first segment is the prefix
	}
	for _, c := range cases {
		if got := stripUUID(c.id, c.uuid); got != c.want {
			t.Errorf("stripUUID(%q,%q) = %q, want %q", c.id, c.uuid, got, c.want)
		}
	}
}

func TestLocalPathFor(t *testing.T) {
	in := &pb.Path{WellIds: []string{"fs/1", "fs/2", "proc/3"}}
	out := localPathFor(in, "fs")
	want := []string{"1", "2", "proc/3"}
	if len(out.WellIds) != len(want) {
		t.Fatalf("len = %d, want %d", len(out.WellIds), len(want))
	}
	for i := range want {
		if out.WellIds[i] != want[i] {
			t.Errorf("seg %d = %q, want %q", i, out.WellIds[i], want[i])
		}
	}
	if localPathFor(nil, "fs") != nil {
		t.Error("localPathFor(nil) should be nil")
	}
}

// TestPluginRoute covers the routing decision: only a qualified id whose uuid
// names a registered non-localdb plugin routes away from the local store.
func TestPluginRoute(t *testing.T) {
	reg := plugin.NewRegistry()
	// A nil client is fine here — the test only checks the routing decision,
	// not a forwarded call.
	reg.Register("fsuuid", "fs", nil, nil)
	h := &connectHandler{srv: &Server{localdbUUID: "localdb", pluginReg: reg}}

	cases := []struct {
		name      string
		id        string
		wantOK    bool
		wantLocal string
		wantUUID  string
	}{
		{"foreign plugin", "fsuuid/7", true, "7", "fsuuid"},
		{"bare id", "42", false, "", ""},
		{"localdb-qualified", "localdb/42", false, "", ""},
		{"unregistered plugin", "ghost/3", false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, local, uuid, ok := h.pluginRoute(c.id)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && (local != c.wantLocal || uuid != c.wantUUID) {
				t.Errorf("local=%q uuid=%q, want local=%q uuid=%q", local, uuid, c.wantLocal, c.wantUUID)
			}
		})
	}
}

// TestPluginRouteNoRegistry: with no registry wired, everything routes local.
func TestPluginRouteNoRegistry(t *testing.T) {
	h := &connectHandler{srv: &Server{localdbUUID: "localdb"}}
	if _, _, _, ok := h.pluginRoute("fsuuid/7"); ok {
		t.Error("pluginRoute should be false with no registry")
	}
}
