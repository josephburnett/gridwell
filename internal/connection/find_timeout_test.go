package connection

import (
	"context"
	"github.com/josephburnett/gridwell/internal/namespace"
	"strings"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// hangingFindClient blocks Find until the caller's context dies: what a
// wedged ssh tunnel looks like to the fan-out.
type hangingFindClient struct {
	namespace.Namespace
}

func (hangingFindClient) Find(ctx context.Context, _ *gridwellv1.FindRequest) (*gridwellv1.FindResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type answeringFindClient struct {
	namespace.Namespace
}

func (answeringFindClient) Find(context.Context, *gridwellv1.FindRequest) (*gridwellv1.FindResponse, error) {
	return &gridwellv1.FindResponse{Results: []*gridwellv1.FindResult{{
		Tile: &gridwellv1.Tile{Id: "farplug/7", GridId: "farplug/1", Kind: "text"},
	}}}, nil
}

// One hung connection must not stall the whole federated find: each hop is
// bounded by rpc.FindHopTimeout, the same owner the server's per-plugin
// fan-out reads. Without the bound, a hanging hop holds the caller's unbounded
// context forever.
func TestFindFanOutBoundsEachHop(t *testing.T) {
	s := newTestServer(t, openStore(t))
	s.mu.Lock()
	s.live["dead"] = &liveConn{client: hangingFindClient{}}
	s.live["fine"] = &liveConn{client: answeringFindClient{}}
	s.mu.Unlock()

	done := make(chan *gridwellv1.FindResponse, 1)
	go func() {
		resp, serr := s.Find(context.Background(), &gridwellv1.FindRequest{Query: "note"})
		if serr != nil {
			t.Errorf("Find: %v", serr)
		}
		done <- resp
	}()
	select {
	case resp := <-done:
		if len(resp.GetResults()) != 1 || resp.Results[0].Tile.Id != "fine/farplug/7" {
			t.Fatalf("want the live hop's qualified result, got %+v", resp.GetResults())
		}
		if sk := resp.GetSkipped(); len(sk) != 1 || sk[0].Namespace != "dead" || !strings.Contains(sk[0].Reason, "did not answer") {
			t.Fatalf("skipped = %+v, want the hung connection named with its timeout", sk)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("find stalled on the hung connection — no per-hop bound")
	}
}
