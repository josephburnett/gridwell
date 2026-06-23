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

// TestRoute covers the uniform routing decision: a qualified id resolves to the
// named plugin; a bare id resolves to the primary; an unregistered uuid errors.
func TestRoute(t *testing.T) {
	reg := plugin.NewRegistry()
	// nil clients are fine — the test only checks the routing decision.
	reg.Register("fsuuid", "fs", nil, nil)
	reg.Register("primary", "localdb", nil, nil)
	h := &connectHandler{srv: &Server{primaryUUID: "primary", pluginReg: reg}}

	cases := []struct {
		name, id, wantLocal, wantUUID string
		wantErr                       bool
	}{
		{"foreign plugin", "fsuuid/7", "7", "fsuuid", false},
		{"bare → primary", "42", "42", "primary", false},
		{"primary-qualified", "primary/9", "9", "primary", false},
		{"unregistered", "ghost/3", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, local, uuid, err := h.route(c.id)
			if c.wantErr {
				if err == nil {
					t.Fatal("expected error for unregistered plugin")
				}
				return
			}
			if err != nil {
				t.Fatalf("route: %v", err)
			}
			if local != c.wantLocal || uuid != c.wantUUID {
				t.Errorf("local=%q uuid=%q, want %q/%q", local, uuid, c.wantLocal, c.wantUUID)
			}
		})
	}
}
