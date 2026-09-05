package connection

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/deadref"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// landingClient is a far node that answers Info with one root and serves the
// grids it is asked for: enough to learn a landing over and route through.
type landingClient struct {
	namespace.Namespace
	root string
}

func (c landingClient) Info(context.Context, *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return &gridwellv1.InfoResponse{RootGridId: c.root}, nil
}

func (c landingClient) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, _ func(*gridwellv1.Event) error) error {
	<-ctx.Done()
	return ctx.Err()
}

func (c landingClient) Handshake(context.Context, *gridwellv1.HandshakeRequest) (*gridwellv1.HandshakeResponse, error) {
	return &gridwellv1.HandshakeResponse{}, nil
}

func (c landingClient) GetGrid(_ context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	return &gridwellv1.GetGridResponse{Grid: &gridwellv1.Grid{Id: req.GridId}}, nil
}

// sharedConnDB opens the connections table on a handle the test owns, the
// production shape: closing a transport leaves the store open for the next
// boot.
func sharedConnDB(t *testing.T) *DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "conns.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := NewDB(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// A connection's name is dropped from server.yaml and later declared again.
// Three boots over ONE store, across the whole seam the reversal touches: the
// boot reconcile, the row, the routing table, and the roster a client reads a
// reference's deadness from.
//
// Boot 1 declares rtb and learns its landing; a mount reference into it is
// live. Boot 2 drops the stanza: the row survives with its landing, nothing
// is tombstoned, the namespace stops resolving, and the reference is DEAD
// through the roster — a state, not an error, and never a sweep. Boot 3
// re-declares it and everything comes back on the same landing.
func TestConnectionSurvivesRemoveThenRestore(t *testing.T) {
	ctx := context.Background()
	db := sharedConnDB(t)

	const nodeID = "lnode1"
	rtb := []config.ConnectionConfig{{Name: "rtb", Addr: "/far/federation.sock"}}
	dialer := func(dial.Config) (namespace.Namespace, func(), error) {
		return landingClient{root: "rnode1/7"}, func() {}, nil
	}
	// The stored reference a mount leaves behind: a link tile into the
	// connection's namespace, as the user's grid holds it.
	ref := &rpc.Tile{Reference: true, LinkTargetID: nodeID + "/rtb/rnode1/7"}
	roster := func(s *Server) []rpc.PluginInfo {
		var out []rpc.PluginInfo
		for _, r := range s.Rows(ctx) {
			out = append(out, rpc.PluginInfo{UUID: rpc.QualifyID(nodeID, r.Name), RootGridID: r.RootGridID})
		}
		// A node always declares its own home, so the roster is never empty
		// and deadref always has a declaration to answer from.
		return append(out, rpc.PluginInfo{UUID: nodeID})
	}

	// Boot 1: declared, landing learned, reference live.
	s1, err := New(db, dialer, "", rtb, nil)
	if err != nil {
		t.Fatal(err)
	}
	s1.ConnectAll(ctx)
	if r, _ := db.Get(ctx, "rtb"); r.RemoteRoot != "rnode1/7" {
		t.Fatalf("boot 1 remote_root = %q, want the learned landing", r.RemoteRoot)
	}
	if deadref.DeadTile(ref, roster(s1), nodeID) {
		t.Fatal("boot 1: a declared connection's reference must be live")
	}
	if _, err := s1.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: "rtb/rnode1/7"}); err != nil {
		t.Fatalf("boot 1 GetGrid through the connection: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	// Boot 2: the stanza is gone. Nothing is retired; the row and its landing
	// stay; the namespace does not resolve; the reference is dead.
	s2, err := New(db, dialer, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := db.Get(ctx, "rtb")
	if err != nil {
		t.Fatalf("the undeclared row must survive: %v", err)
	}
	if r.Deleted {
		t.Fatal("boot 2 tombstoned a name retired_names never named")
	}
	if r.RemoteRoot != "rnode1/7" {
		t.Fatalf("boot 2 remote_root = %q, want the landing kept", r.RemoteRoot)
	}
	if rows := s2.Rows(ctx); len(rows) != 0 {
		t.Fatalf("boot 2 rows = %+v, want none declared", rows)
	}
	_, err = s2.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: "rtb/rnode1/7"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("boot 2 GetGrid = %v, want NotFound: an undeclared namespace resolves to nothing", err)
	}
	// Unresolvable is not GONE: a Probe on the reference must never tell the
	// sweeper the tile is gone, or removing a stanza would delete the user's
	// links.
	pr, err := s2.Probe(ctx, &gridwellv1.ProbeRequest{TileId: "rtb/rnode1/7"})
	if err == nil && pr.GetPresence() == gridwellv1.ProbeResponse_PRESENCE_GONE {
		t.Fatal("boot 2 Probe said GONE for a name that was never retired")
	}
	if !deadref.DeadTile(ref, roster(s2), nodeID) {
		t.Fatal("boot 2: a reference into a namespace the node no longer declares is dead")
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}

	// Boot 3: declared again. Everything comes back on the same landing.
	s3, err := New(db, dialer, "", rtb, nil)
	if err != nil {
		t.Fatalf("boot 3 refused a name only absence ever retired: %v", err)
	}
	t.Cleanup(func() { _ = s3.Close() })
	s3.ConnectAll(ctx)
	rows := s3.Rows(ctx)
	if len(rows) != 1 || rows[0].RootGridID != "rtb/rnode1/7" {
		t.Fatalf("boot 3 rows = %+v, want rtb on its remembered landing", rows)
	}
	if deadref.DeadTile(ref, roster(s3), nodeID) {
		t.Fatal("boot 3: the reference must resolve again")
	}
	if _, err := s3.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: "rtb/rnode1/7"}); err != nil {
		t.Fatalf("boot 3 GetGrid through the restored connection: %v", err)
	}
}
