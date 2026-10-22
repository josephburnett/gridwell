package pluginhost_test

import (
	"context"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// declaresMove declares move and answers Move Unimplemented, a broken
// declaration.
type declaresMove struct {
	lackingPlugin
}

func (p *declaresMove) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "mover", Move: true,
		Grids: []*pluginv1.PluginGrid{{Id: "c", Grid: "c"}, {Id: "o", Grid: "o"}}}, nil
}

func (p *declaresMove) List(_ context.Context, req *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	resp := &pluginv1.ListResponse{Authoritative: true}
	if req.Grid == "c" {
		resp.Entries = []*pluginv1.Entry{{Key: "a", Kind: rpc.KindText, Label: "a"}}
	}
	return resp, nil
}

func (p *declaresMove) Move(context.Context, *pluginv1.MoveRequest) (*pluginv1.MoveResponse, error) {
	return nil, p.lack("Move")
}

// A plugin that declares move and answers Unimplemented is refused with its
// answer, asked once for the process, and the entry stays where it was.
func TestABrokenMoveDeclarationRefusesWithTheAnswer(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	impl := &declaresMove{lackingPlugin{asks: map[string]int{}}}
	cp, closer, err := plugintest.Loopback(impl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	a := pluginhost.New(cp, st.Namespace("p1"), nil)
	id := rpc.EntryTileID("c", "a")
	for range 2 {
		_, err := a.MoveTile(t.Context(), &gridwellv1.MoveTileRequest{TileId: id, GridId: rpc.EntryGridID("o"), W: 1, H: 1})
		if status.Code(err) != codes.Unimplemented {
			t.Fatalf("move = %v, want the plugin's Unimplemented", err)
		}
	}
	if got := impl.count("Move"); got != 1 {
		t.Fatalf("Move asked %d times of one process, want once", got)
	}
	if _, err := a.GetTile(t.Context(), &gridwellv1.GetTileRequest{TileId: id}); err != nil {
		t.Fatalf("the entry left its grid: %v", err)
	}
}
