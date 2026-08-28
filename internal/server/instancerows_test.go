package server

// v2 #269: a parameterized plugin's instances join the ListPlugins
// answer as menu rows of their own — synthesized from the instance grid
// the plugin already declares, no kind consulted. This is the seam the
// "why don't I see rtb in the menu" cutover gap lived on.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// fakeParameterized declares an instance grid holding one connected
// instance (child learned) and one pending one (no child, a status).
type fakeParameterized struct {
	pb.UnimplementedGridwellServer
	// brokenInstanceGrid makes the instance-grid read fail — the one
	// case that keeps the plugin's own row listed.
	brokenInstanceGrid bool
}

func (f fakeParameterized) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return &pb.InfoResponse{Kind: "fakeparam", DisplayName: "things", InstanceGridId: "0"}, nil
}

func (f fakeParameterized) GetGrid(_ context.Context, req *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	if f.brokenInstanceGrid {
		return nil, fmt.Errorf("instance store unavailable")
	}
	return &pb.GetGridResponse{
		Grid: &pb.Grid{Id: req.GridId},
		Tiles: []*pb.Tile{
			{Id: "7", GridId: "0", Kind: "well", AltText: "rtb",
				ChildGridId: "conn123/rnode99/1", ViewX: 2, ViewY: -1, ViewZoom: 1.5},
			{Id: "8", GridId: "0", Kind: "well", AltText: "pending",
				StatusDetail: "the remote hasn't answered"},
		},
	}, nil
}

func listFor(t *testing.T, impl pb.GridwellServer) []rpc.PluginInfo {
	t.Helper()
	client, closer, err := plugin.ServeInProcess(impl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register("fakeuux", "fakeparam", client, nil)
	hs := serveWeb(t, mustNew(t, reg, Config{}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	pl, err := cl.ListPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return pl.Plugins
}

func TestParameterizedInstancesJoinTheMenu(t *testing.T) {
	// The instances ARE the menu rows — the plugin's own row is replaced
	// (one icon per configured thing; the picker is gone, 2026-08-23).
	plugins := listFor(t, fakeParameterized{})
	if len(plugins) != 2 {
		t.Fatalf("want ONLY the 2 instance rows, got %d: %+v", len(plugins), plugins)
	}
	connected, pending := plugins[0], plugins[1]
	// The connected instance: a chained namespace, its child as the root
	// (click descends), its framing carried, its label its own.
	if connected.UUID != "fakeuux/conn123" || connected.RootGridID != "fakeuux/conn123/rnode99/1" {
		t.Fatalf("connected row ids: %+v", connected)
	}
	if connected.Label != "rtb" || connected.RootViewZoom != 1.5 {
		t.Fatalf("connected row label/framing: %+v", connected)
	}
	// The pending instance: listed (never blanked), rootless and inert,
	// its status riding InfoError so the menu can say why.
	if pending.Label != "pending" || pending.RootGridID != "" || pending.InfoError != "the remote hasn't answered" {
		t.Fatalf("pending row: %+v", pending)
	}
}

func TestUnreadableInstanceGridKeepsThePluginRow(t *testing.T) {
	// A configured plugin must never blank silently: when the instance
	// grid cannot be read, the plugin's own (rootless-inert) row stays.
	plugins := listFor(t, fakeParameterized{brokenInstanceGrid: true})
	if len(plugins) != 1 {
		t.Fatalf("want the plugin's own row alone, got %d: %+v", len(plugins), plugins)
	}
	if plugins[0].InstanceGridID == "" || plugins[0].RootGridID != "" {
		t.Fatalf("row 0 should be the parameterized plugin's own: %+v", plugins[0])
	}
	// The read error must ride the row: without it, "instance store down"
	// and "healthy but rootless" are indistinguishable on the wire — the
	// same class issue #47 fixed for the Info handshake, one read further
	// in. The picker that used to surface this on descent is gone, so the
	// row is the only place the user can learn why.
	if plugins[0].InfoError == "" || !strings.Contains(plugins[0].InfoError, "instance store unavailable") {
		t.Fatalf("row 0 must carry the instance-grid read error, got %q", plugins[0].InfoError)
	}
}
