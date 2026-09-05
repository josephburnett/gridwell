package pluginhost_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// deletePlugin is a plugin whose Delete always succeeds and whose Probe
// answers whatever the case under test says — the one question that decides
// whether the row goes with the source thing.
//
// Its listing NEVER stops naming the key, in every case, so no listing-side
// rule can retire anything: a sweep only retires what an authoritative listing
// omits, and the probe arm only reaches a row the listing left out. Whatever
// the row does here, DeleteTile decided it.
type deletePlugin struct {
	pluginv1.UnimplementedPluginServer
	presence pluginv1.ProbeResponse_Presence
	probeErr error

	mu      sync.Mutex
	deletes int
}

func (p *deletePlugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "deleteish", DisplayName: "deleteish", RootContext: "r"}, nil
}

func (p *deletePlugin) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	return &pluginv1.ListResponse{
		Entries:       []*pluginv1.Entry{{Key: "todo:1", Kind: "text", Label: "a todo"}},
		Authoritative: true,
	}, nil
}

func (p *deletePlugin) Delete(context.Context, *pluginv1.DeleteRequest) (*pluginv1.DeleteResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deletes++
	return &pluginv1.DeleteResponse{}, nil
}

func (p *deletePlugin) Probe(context.Context, *pluginv1.ProbeRequest) (*pluginv1.ProbeResponse, error) {
	if p.probeErr != nil {
		return nil, p.probeErr
	}
	return &pluginv1.ProbeResponse{Presence: p.presence}, nil
}

func (p *deletePlugin) deleteCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deletes
}

// A minted row retires only when its source says the key is gone. Delete is
// the plugin's verb and the plugin decides what it means: fs removes the file,
// gitlab marks the todo done and keeps the tile. So the node asks the same
// question it asks on the listing path — Probe — and retires the row only on a
// definitive GONE. The row is where the placement and every stored reference
// live, so retiring one whose thing is still there loses both.
func TestDeleteRetiresOnlyWhatTheSourceSaysIsGone(t *testing.T) {
	for _, tc := range []struct {
		name     string
		presence pluginv1.ProbeResponse_Presence
		probeErr error
		wantRow  bool
	}{
		{"a delete that transforms keeps the row", pluginv1.ProbeResponse_PRESENCE_PRESENT, nil, true},
		{"a delete that removes retires the row", pluginv1.ProbeResponse_PRESENCE_GONE, nil, false},
		{"an unreachable source keeps the row", 0, status.Error(codes.Unavailable, "source unreachable"), true},
		{"a source that cannot say keeps the row", pluginv1.ProbeResponse_PRESENCE_UNSPECIFIED, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = memStore.Close() })
			impl := &deletePlugin{presence: tc.presence, probeErr: tc.probeErr}
			cp, cpCloser, err := plugintest.Loopback(impl)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cpCloser)
			client := pluginhost.New(cp, memStore.Namespace("p1"), nil)
			ctx := context.Background()

			info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
			if err != nil {
				t.Fatal(err)
			}
			before, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
			if err != nil {
				t.Fatal(err)
			}
			if len(before.Tiles) != 1 {
				t.Fatalf("listing = %v, want the one entry", before.Tiles)
			}
			// The durable touch: a move is what mints the row, and the row is
			// what the delete must decide about.
			moved, err := client.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
				TileId: before.Tiles[0].Id, X: 5, Y: 5, W: 3, H: 2,
			})
			if err != nil {
				t.Fatal(err)
			}
			rowID := moved.GetTile().GetId()

			if _, err := client.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: rowID}); err != nil {
				t.Fatalf("DeleteTile: %v", err)
			}
			if got := impl.deleteCount(); got != 1 {
				t.Fatalf("the plugin saw %d deletes, want exactly the one gesture", got)
			}

			tile, err := client.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: rowID})
			if !tc.wantRow {
				if status.Code(err) != codes.NotFound {
					t.Fatalf("GetTile on the retired row = (%v, %v), want NotFound", tile.GetTile(), err)
				}
				return
			}
			if err != nil {
				t.Fatalf("the row went with the delete: GetTile %s: %v", rowID, err)
			}
			if got := tile.GetTile(); got.GetX() != 5 || got.GetY() != 5 || got.GetW() != 3 || got.GetH() != 2 {
				t.Errorf("placement after the delete = %+v, want the 5,5 3x2 the user left", got)
			}
			// And the next listing still names the entry BY THAT ROW: an entry
			// answered at a derived address again is a fresh identity, which is
			// what breaks every stored reference to it.
			after, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
			if err != nil {
				t.Fatal(err)
			}
			if len(after.Tiles) != 1 || after.Tiles[0].GetId() != rowID {
				t.Fatalf("listing after the delete = %v, want the one entry on row %s", after.Tiles, rowID)
			}
			if after.Tiles[0].GetX() != 5 || after.Tiles[0].GetY() != 5 {
				t.Errorf("the listing put the entry back at its hint: %+v", after.Tiles[0])
			}
		})
	}
}
