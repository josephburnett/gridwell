package connection

import (
	"context"
	"slices"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// hungFar never answers its first SetView and records every later one.
type hungFar struct {
	namespace.Unimplemented
	calls chan *gridwellv1.SetViewRequest
	first bool
}

func (h *hungFar) SetView(ctx context.Context, req *gridwellv1.SetViewRequest) (*gridwellv1.SetViewResponse, error) {
	if !h.first {
		h.first = true
		<-ctx.Done()
		return nil, ctx.Err()
	}
	h.calls <- req
	return &gridwellv1.SetViewResponse{}, nil
}

// A far node that stops answering holds one share for tellFarWait and no
// longer: the newer share behind it still reaches the node once it answers,
// as a view labelled as another node's under the connection's session.
func TestTellFarIsBoundedByTellFarWait(t *testing.T) {
	old := tellFarWait
	tellFarWait = 50 * time.Millisecond
	t.Cleanup(func() { tellFarWait = old })

	s := newTestServer(t, openStore(t))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	far := &hungFar{calls: make(chan *gridwellv1.SetViewRequest, 4)}
	if _, err := s.SetView(ctx, &gridwellv1.SetViewRequest{ShownGridIds: []string{"geneva/p/1"}}); err != nil {
		t.Fatal(err)
	}
	go s.tellFar(ctx, "geneva", far)
	if _, err := s.SetView(ctx, &gridwellv1.SetViewRequest{ShownGridIds: []string{"geneva/p/2", "other/p/3"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-far.calls:
		if !slices.Equal(got.GetShownGridIds(), []string{"p/2"}) {
			t.Errorf("the far node was told %v, want its own share [p/2]", got.GetShownGridIds())
		}
		if got.GetLabel() != farLabel || got.GetSession() == "" || len(got.GetPanes()) != 0 {
			t.Errorf("the far node was told %v, want a pane-less %q view under a session", got, farLabel)
		}
	case <-time.After(20 * tellFarWait):
		t.Fatal("the newer share never reached the far node past the hung call")
	}
}
