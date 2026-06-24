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
	cases := []struct {
		name string
		in   []string
		uuid string
		want []string
	}{
		{"all same plugin", []string{"fs/1", "fs/2"}, "fs", []string{"1", "2"}},
		{"trailing run after boundary", []string{"p/1", "fs/2", "fs/3"}, "fs", []string{"2", "3"}},
		{"leaf in other plugin → empty", []string{"fs/1", "fs/2", "proc/3"}, "fs", []string{}},
		{"single", []string{"p/9"}, "p", []string{"9"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := localPathFor(&pb.Path{WellIds: c.in}, c.uuid)
			if len(out.WellIds) != len(c.want) {
				t.Fatalf("got %v, want %v", out.WellIds, c.want)
			}
			for i := range c.want {
				if out.WellIds[i] != c.want[i] {
					t.Errorf("seg %d = %q, want %q", i, out.WellIds[i], c.want[i])
				}
			}
		})
	}
	if localPathFor(nil, "fs") != nil {
		t.Error("localPathFor(nil) should be nil")
	}
}

// TestRoute covers the uniform routing decision: a qualified id resolves to the
// named plugin; a bare id errors (no root to fall back to); an unregistered
// uuid errors.
func TestRoute(t *testing.T) {
	reg := plugin.NewRegistry()
	// nil clients are fine — the test only checks the routing decision.
	reg.Register("fsuuid", "fs", nil, nil)
	reg.Register("localuuid", "localdb", nil, nil)
	h := &connectHandler{srv: &Server{pluginReg: reg}}

	cases := []struct {
		name, id, wantLocal, wantUUID string
		wantErr                       bool
	}{
		{"foreign plugin", "fsuuid/7", "7", "fsuuid", false},
		{"bare → error", "42", "", "", true},
		{"qualified localdb", "localuuid/9", "9", "localuuid", false},
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
