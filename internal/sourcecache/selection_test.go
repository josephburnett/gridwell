package sourcecache

import (
	"context"
	"slices"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

func (f *framingSource) SetSelection(context.Context, *pb.SetSelectionRequest) (*pb.SetSelectionResponse, error) {
	if f.dark() {
		return nil, f.offline()
	}
	return &pb.SetSelectionResponse{}, nil
}

// cachedSelection is the far grid's selection as the layer answers it
// with the source gone.
func cachedSelection(t *testing.T, cc *Layer, src *framingSource) []string {
	t.Helper()
	src.setDown(true)
	defer src.setDown(false)
	g, err := cc.GetGrid(context.Background(), &pb.GetGridRequest{GridId: farHome})
	if err != nil {
		t.Fatalf("GetGrid while dark: %v", err)
	}
	return g.GetGrid().GetSelection()
}

// A far grid's selection reads while its machine is gone as the source last
// held it: from the source's event, and from a write it accepted whose event
// never came.
func TestAFarGridsSelectionIsCached(t *testing.T) {
	cc, src := framingFixture(t)
	cc.applyEvent(context.Background(), rpc.SelectionEvent(farHome, []string{farDoor}))
	if got := cachedSelection(t, cc, src); !slices.Equal(got, []string{farDoor}) {
		t.Errorf("after the event the cached selection = %v, want [%s]", got, farDoor)
	}
	if _, err := cc.SetSelection(context.Background(), &pb.SetSelectionRequest{GridId: farHome,
		Remove: []string{farDoor}}); err != nil {
		t.Fatal(err)
	}
	if got := cachedSelection(t, cc, src); len(got) != 0 {
		t.Errorf("after an accepted remove the cached selection = %v, want none", got)
	}
	if _, err := cc.SetSelection(context.Background(), &pb.SetSelectionRequest{GridId: farHome,
		Add: []string{farDoor}}); err != nil {
		t.Fatal(err)
	}
	if got := cachedSelection(t, cc, src); !slices.Equal(got, []string{farDoor}) {
		t.Errorf("after an accepted add the cached selection = %v, want [%s]", got, farDoor)
	}
	if _, err := cc.SetSelection(context.Background(), &pb.SetSelectionRequest{GridId: farHome, Clear: true}); err != nil {
		t.Fatal(err)
	}
	if got := cachedSelection(t, cc, src); len(got) != 0 {
		t.Errorf("after an accepted clear the cached selection = %v, want none", got)
	}
}
