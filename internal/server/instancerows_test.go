package server

// v2 #269: a parameterized plugin's instances join the ListPlugins
// answer as menu rows of their own — synthesized from the instance grid
// the plugin already declares, no kind consulted. This is the seam the
// "why don't I see rtb in the menu" cutover gap lived on.

import (
	"context"
	"net/http/httptest"
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
	withSchema bool
}

func (f fakeParameterized) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	resp := &pb.InfoResponse{Kind: "fakeparam", DisplayName: "things", InstanceGridId: "0"}
	if f.withSchema {
		resp.CreateSchemas = map[string]string{"well": "{}"}
	}
	return resp, nil
}

func (fakeParameterized) GetGrid(_ context.Context, req *pb.GetGridRequest) (*pb.GetGridResponse, error) {
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
	hs := httptest.NewServer(New(reg, Config{}).Handler())
	t.Cleanup(hs.Close)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	pl, err := cl.ListPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return pl.Plugins
}

func TestParameterizedInstancesJoinTheMenu(t *testing.T) {
	// Legacy world (a creation schema — the picker still manages): the
	// plugin's own row stays, instances after it.
	plugins := listFor(t, fakeParameterized{withSchema: true})
	if len(plugins) != 3 {
		t.Fatalf("want the parameterized row + 2 instance rows, got %d: %+v", len(plugins), plugins)
	}
	param, connected, pending := plugins[0], plugins[1], plugins[2]
	if param.InstanceGridID == "" || param.RootGridID != "" {
		t.Fatalf("row 0 should be the parameterized plugin: %+v", param)
	}
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

func TestConfigManagedInstancesReplaceThePluginRow(t *testing.T) {
	// v2 world (no creation schema — the picker has no job left): one
	// icon per configured thing; the plugin's own row disappears behind
	// its instances.
	plugins := listFor(t, fakeParameterized{})
	if len(plugins) != 2 {
		t.Fatalf("want ONLY the 2 instance rows, got %d: %+v", len(plugins), plugins)
	}
	for _, p := range plugins {
		if p.InstanceGridID != "" {
			t.Fatalf("the parameterized plugin's own row leaked through: %+v", p)
		}
	}
	if plugins[0].Label != "rtb" || plugins[1].Label != "pending" {
		t.Fatalf("instance rows: %+v", plugins)
	}
}
