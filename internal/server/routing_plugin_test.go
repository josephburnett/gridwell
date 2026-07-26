package server

import (
	"testing"

	"github.com/josephburnett/gridwell/internal/plugin"
)

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
