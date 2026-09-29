package connection

import (
	"context"
	"slices"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// hungFar never answers its first SetInterest and records every later one.
type hungFar struct {
	namespace.Unimplemented
	calls chan []string
	first bool
}

func (h *hungFar) SetInterest(ctx context.Context, req *gridwellv1.SetInterestRequest) (*gridwellv1.SetInterestResponse, error) {
	if !h.first {
		h.first = true
		<-ctx.Done()
		return nil, ctx.Err()
	}
	h.calls <- req.GridIds
	return &gridwellv1.SetInterestResponse{}, nil
}

// A far node that stops answering holds one set for tellFarWait and no
// longer: the newer set behind it still reaches the node once it answers.
func TestTellFarIsBoundedByTellFarWait(t *testing.T) {
	old := tellFarWait
	tellFarWait = 50 * time.Millisecond
	t.Cleanup(func() { tellFarWait = old })

	s := newTestServer(t, openConnDB(t))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	far := &hungFar{calls: make(chan []string, 4)}
	if _, err := s.SetInterest(ctx, &gridwellv1.SetInterestRequest{GridIds: []string{"geneva/p/1"}}); err != nil {
		t.Fatal(err)
	}
	go s.tellFar(ctx, "geneva", far)
	if _, err := s.SetInterest(ctx, &gridwellv1.SetInterestRequest{GridIds: []string{"geneva/p/2", "other/p/3"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-far.calls:
		if !slices.Equal(got, []string{"p/2"}) {
			t.Errorf("the far node was told %v, want its own share [p/2]", got)
		}
	case <-time.After(20 * tellFarWait):
		t.Fatal("the newer set never reached the far node past the hung call")
	}
}
