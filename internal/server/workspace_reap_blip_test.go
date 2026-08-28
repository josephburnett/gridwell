package server

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// blipClient forwards to the real plugin, but after the delete of armID
// every GetTile answers Unavailable — a transport blip at exactly the
// moment the handler confirms whether the deleted workspace row is gone.
type blipClient struct {
	pb.GridwellClient
	armID string
	blip  atomic.Bool
}

func (c *blipClient) DeleteTile(ctx context.Context, req *pb.DeleteTileRequest, opts ...grpc.CallOption) (*pb.DeleteTileResponse, error) {
	resp, err := c.GridwellClient.DeleteTile(ctx, req, opts...)
	if err == nil && req.TileId == c.armID {
		c.blip.Store(true)
	}
	return resp, err
}

func (c *blipClient) GetTile(ctx context.Context, req *pb.GetTileRequest, opts ...grpc.CallOption) (*pb.TileResponse, error) {
	if c.blip.Load() && req.TileId == c.armID {
		return nil, status.Error(codes.Unavailable, "blip")
	}
	return c.GridwellClient.GetTile(ctx, req, opts...)
}

// A transport blip on the confirming read must NOT reap: "row unreadable
// right now" is not "row destroyed", and the reap is unrecoverable
// (shells killed, scratch rows deleted) while the trash may in fact be
// keeping the workspace for a restore. Only an explicit NotFound may
// reap.
func TestWorkspaceDeleteBlipDoesNotReap(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	inner, closer, err := plugin.ServeInProcess(local.New(st, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	uuid, err := st.PluginUUID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bare, err := st.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bc := &blipClient{GridwellClient: inner}
	reg := plugin.NewRegistry()
	reg.Register(uuid, "home", bc, nil)
	srv := New(reg, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	root := uuid + "/" + bare

	pt, err := cl.CreatePane(ctx, &rpc.CreatePaneRequest{GridID: root, X: 0, Y: 0, W: 2, H: 2, Label: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	eph, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: g.Grid.ScratchGridID, X: 0, Y: 0, W: 1, H: 1, Data: []byte("# scratch"),
	})
	if err != nil {
		t.Fatal(err)
	}
	layout := `{"v":1,"root":{"pane":{"id":"p1","anchor":"` + root +
		`","cx":0.5,"cy":0.5,"zoom":1,"text_focus":"` + eph.ID + `"}},"focus":"p1"}`
	if _, err := cl.WriteContent(ctx, pt.ID, pt.Version, []byte(layout)); err != nil {
		t.Fatal(err)
	}

	_, paneLocal, _ := rpc.SplitID(pt.ID)
	bc.armID = paneLocal
	if err := cl.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: pt.ID, Version: pt.Version}); err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}
	bc.blip.Store(false)

	if _, err := cl.GetTile(ctx, eph.ID); err != nil {
		t.Fatalf("ephemeral reaped on a transport blip: %v", err)
	}
}
