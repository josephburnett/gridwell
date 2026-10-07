package server

import (
	"context"
	"testing"
)

// The handshake names the build that answers it, for the client's log.
func TestHandshakeNamesTheNodesBuild(t *testing.T) {
	f := newShellDoorFixture(t, Config{}, func(s *Server) { s.build = "node-b" })
	pl, err := f.cl.Handshake(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pl.Build != "node-b" {
		t.Errorf("handshake build = %q, want the node's", pl.Build)
	}
}
