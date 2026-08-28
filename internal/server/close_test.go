package server

import (
	"context"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// New with a NodeID opens an in-process grpc listener for the node grid;
// Close must release it. Before the fix, nodeClose was stored and never
// called by anything — every mobile Start/Stop cycle leaked the
// listener, its grpc server, and the client conn.
func TestCloseTearsDownTheNodeGridListener(t *testing.T) {
	srv := mustNew(t, plugin.NewRegistry(), Config{NodeID: "tnode"})
	if _, err := srv.nodeClient.Info(context.Background(), &pb.InfoRequest{}); err != nil {
		t.Fatalf("node grid unreachable before Close: %v", err)
	}
	srv.Close()
	if _, err := srv.nodeClient.Info(context.Background(), &pb.InfoRequest{}); err == nil {
		t.Fatal("node grid still serving after Close — the in-process listener leaked")
	}
}

// A server without a node grid closes as a no-op.
func TestCloseWithoutNodeGrid(t *testing.T) {
	mustNew(t, plugin.NewRegistry(), Config{}).Close()
}
