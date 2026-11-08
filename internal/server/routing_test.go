package server

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace/namespacetest"
)

// routeMark is a namespace known by its name, so a route says which it took.
type routeMark struct {
	namespacetest.Unimplemented
	name string
}

// TestRoute covers the uniform routing decision: a qualified id resolves to
// home, the files or the transport by its shape under the node's id; a bare
// id errors (no root to fall back to); an undeclared namespace errors.
func TestRoute(t *testing.T) {
	const node = "lnode1"
	reg := &Sources{}
	reg.Home = &routeMark{name: "home"}
	reg.Files = &routeMark{name: "files"}
	reg.Transport = &routeMark{name: "transport"}
	rt := &router{srv: &Server{src: *reg, cfg: Config{ID: node}}}
	file := rpc.KeyTileID("/a")

	cases := []struct {
		name, id, wantNS, wantLocal string
		wantTransit, wantErr        bool
	}{
		{"home", node + "/9", "home", "9", false, false},
		{"files", node + "/" + file, "files", file, false, false},
		{"connection", node + "/geneva/rnode1/5", "transport", "geneva/rnode1/5", true, false},
		{"bare → error", "42", "", "", false, true},
		{"undeclared", "ghost/3", "", "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ns, local, _, transit, err := rt.route(c.id)
			if c.wantErr {
				if err == nil {
					t.Fatalf("route(%q) answered %v, want an error", c.id, ns)
				}
				return
			}
			if err != nil {
				t.Fatalf("route: %v", err)
			}
			if got := ns.(*routeMark).name; got != c.wantNS || local != c.wantLocal || transit != c.wantTransit {
				t.Errorf("route(%q) = %s %q transit %v, want %s %q transit %v", c.id, got, local, transit, c.wantNS, c.wantLocal, c.wantTransit)
			}
		})
	}
}
