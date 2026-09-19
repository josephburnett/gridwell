package server

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// brokenShellHost is a namespace whose PTY host cannot be asked: the tmux
// socket refuses. That is not a dead session.
type brokenShellHost struct{ namespace.Unimplemented }

func (brokenShellHost) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return &pb.InfoResponse{DisplayName: "h", RootGridId: "1"}, nil
}

func (brokenShellHost) ShellSessionAlive(context.Context, *pb.ShellSessionAliveRequest) (*pb.ShellSessionAliveResponse, error) {
	return nil, status.Error(codes.Internal, "tmux has-session: socket refused")
}

// The router carries the owner's probe failure to the client rather than
// folding it into "dead": a dead answer hides the refresh affordance with
// nothing said, and the client's probe-failure notice exists for exactly this
// class. Only disable_shells is a verdict of the router's own.
func TestShellSessionAliveFailureReachesTheClient(t *testing.T) {
	reg := plugin.NewRegistry()
	reg.Register("h1", "home", brokenShellHost{}, nil)
	srv := mustNew(t, reg, Config{ID: "h1"})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL)

	_, err := cl.ShellSessionAlive(context.Background(), "h1/1")
	if err == nil || !strings.Contains(err.Error(), "socket refused") {
		t.Fatalf("err = %v; the owner's failure must reach the client with its reason", err)
	}

	// A node with shells off answers dead, a verdict, and the owner is never
	// asked.
	srv2 := mustNew(t, reg, Config{ID: "h1", DisableShells: true})
	hs2 := serveWeb(t, srv2)
	cl2 := rpc.NewClient(hs2.Client(), hs2.URL)
	alive, err := cl2.ShellSessionAlive(context.Background(), "h1/1")
	if err != nil || alive {
		t.Fatalf("disabled shells: alive=%v err=%v, want dead and no error", alive, err)
	}
}
