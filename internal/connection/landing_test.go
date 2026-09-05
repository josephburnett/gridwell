package connection

import (
	"context"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// landingDialer lands every connection on one far node.
func landingDialer(root string) Dialer {
	return func(dial.Config) (namespace.Namespace, func(), error) {
		return landingClient{root: root}, func() {}, nil
	}
}

// movingClient is a far end whose home can change under a live transport:
// the socket repointed at another node, or the right one restored.
type movingClient struct {
	landingClient
	root *string
}

func (c movingClient) Info(context.Context, *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return &gridwellv1.InfoResponse{RootGridId: *c.root}, nil
}

// A connection name is bound to the node it landed on, because every stored
// reference through it was written against that landing. When the far end
// answers a DIFFERENT home — the socket repointed, the far home rebuilt with a
// fresh id — the transport refuses to serve the connection and says why. The
// stored landing is never overwritten: it is what the user's references name,
// and rewriting it would silently re-point every one of them at another
// node's tiles.
func TestLandingCheckRefusesADifferentNode(t *testing.T) {
	ctx := context.Background()
	db := sharedConnDB(t)
	if err := db.Ensure(ctx, "rtb"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRemoteRoot(ctx, "rtb", "rnode1/7"); err != nil {
		t.Fatal(err)
	}
	conns := []config.ConnectionConfig{{Name: "rtb", Addr: "/s"}}

	// The far end is a different node now. Nothing here is a first learn, so
	// this is the revalidation: the landing is checked on every reconnect,
	// not only the boot that learned it.
	s, err := New(db, landingDialer("othernode/2"), "", conns, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.ConnectAll(ctx)

	if r, _ := db.Get(ctx, "rtb"); r.RemoteRoot != "rnode1/7" {
		t.Fatalf("stored remote_root = %q — a different node's answer must NEVER overwrite the landing references name", r.RemoteRoot)
	}
	rows := s.Rows(ctx)
	if len(rows) != 1 || !strings.Contains(rows[0].StatusDetail, "lands on a different node") {
		t.Fatalf("row = %+v, want the reason on the connection's own row", rows)
	}
	_, err = s.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: "rtb/rnode1/7"})
	if err == nil || !strings.Contains(err.Error(), "lands on a different node") {
		t.Fatalf("GetGrid = %v, want a loud refusal to serve the connection", err)
	}
	if !strings.Contains(err.Error(), "retire the name or restore the target") {
		t.Fatalf("the refusal must say what to do about it: %v", err)
	}

	// The same connection against the node it was learned on serves, with
	// nothing on its row.
	s2, err := New(db, landingDialer("rnode1/7"), "", conns, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	s2.ConnectAll(ctx)
	rows = s2.Rows(ctx)
	if len(rows) != 1 || rows[0].StatusDetail != "" || rows[0].RootGridID != "rtb/rnode1/7" {
		t.Fatalf("row = %+v, want the matching landing served clean", rows)
	}
	if _, err := s2.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: "rtb/rnode1/7"}); err != nil {
		t.Fatalf("a connection that lands where it always did must serve: %v", err)
	}
}

// A restored target heals without a restart: the check re-asks while the
// connection is refused, so putting the far node back brings the connection
// and its references live again.
func TestLandingCheckHealsWhenTheTargetComesBack(t *testing.T) {
	ctx := context.Background()
	db := sharedConnDB(t)
	if err := db.Ensure(ctx, "rtb"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRemoteRoot(ctx, "rtb", "rnode1/7"); err != nil {
		t.Fatal(err)
	}
	root := "othernode/2"
	s, err := New(db, func(dial.Config) (namespace.Namespace, func(), error) {
		return movingClient{root: &root}, func() {}, nil
	}, "", []config.ConnectionConfig{{Name: "rtb", Addr: "/s"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.ConnectAll(ctx)
	if _, err := s.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: "rtb/rnode1/7"}); err == nil {
		t.Fatal("the mismatched connection must refuse")
	}
	// The operator points the socket back at the right node. The next learn
	// asks again, because a refused landing is never taken as settled.
	root = "rnode1/7"
	if _, err := s.learnRoot(s.conns["rtb"]); err != nil {
		t.Fatalf("the restored landing must verify: %v", err)
	}
	if _, err := s.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: "rtb/rnode1/7"}); err != nil {
		t.Fatalf("the connection must serve again: %v", err)
	}
	if d := s.Rows(ctx)[0].StatusDetail; d != "" {
		t.Fatalf("status detail = %q, want it cleared", d)
	}
}
