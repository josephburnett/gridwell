package connection

import (
	"context"
	"github.com/josephburnett/gridwell/internal/namespace"
	"path/filepath"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// hangingSearchClient blocks Search until the caller's context dies: what a
// wedged ssh tunnel looks like to the fan-out.
type hangingSearchClient struct {
	namespace.Namespace
}

func (hangingSearchClient) Search(ctx context.Context, _ *gridwellv1.SearchRequest) (*gridwellv1.SearchResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type answeringSearchClient struct {
	namespace.Namespace
}

func (answeringSearchClient) Search(context.Context, *gridwellv1.SearchRequest) (*gridwellv1.SearchResponse, error) {
	return &gridwellv1.SearchResponse{Results: []*gridwellv1.SearchResult{{
		Tile: &gridwellv1.Tile{Id: "farplug/7", GridId: "farplug/1", Kind: "text"},
	}}}, nil
}

// One hung connection must not stall the whole federated search: each hop is
// bounded by rpc.SearchHopTimeout, the same owner the server's per-plugin
// fan-out reads. Without the bound, a hanging hop holds the caller's unbounded
// context forever.
func TestSearchFanOutBoundsEachHop(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := newTestServer(t, db)
	s.mu.Lock()
	s.live["dead"] = &liveConn{client: hangingSearchClient{}}
	s.live["fine"] = &liveConn{client: answeringSearchClient{}}
	s.mu.Unlock()

	done := make(chan *gridwellv1.SearchResponse, 1)
	go func() {
		resp, serr := s.Search(context.Background(), &gridwellv1.SearchRequest{Query: "note"})
		if serr != nil {
			t.Errorf("Search: %v", serr)
		}
		done <- resp
	}()
	select {
	case resp := <-done:
		if len(resp.GetResults()) != 1 || resp.Results[0].Tile.Id != "fine/farplug/7" {
			t.Fatalf("want the live hop's qualified result, got %+v", resp.GetResults())
		}
	case <-time.After(8 * time.Second):
		t.Fatal("search stalled on the hung connection — no per-hop bound")
	}
}
