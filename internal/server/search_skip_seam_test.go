package server_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// failingSearch is a namespace whose search index is not answering.
type failingSearch struct{ namespace.Unimplemented }

func (failingSearch) Info(context.Context, *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return &gridwellv1.InfoResponse{}, nil
}

func (failingSearch) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, _ func(*gridwellv1.Event) error) error {
	<-ctx.Done()
	return nil
}

func (failingSearch) Search(context.Context, *gridwellv1.SearchRequest) (*gridwellv1.SearchResponse, error) {
	return nil, status.Error(codes.Unavailable, "the index is not answering")
}

// A free-text search that went without a namespace says which and why, at
// any depth: a far plugin that failed, and then the connection itself once
// the far node is gone. A namespace that does not search is no skip.
func TestASearchSaysWhatItWentWithout(t *testing.T) {
	ctx := context.Background()
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil,
		func(reg *plugin.Registry, _ *store.Store) { reg.Register("pfail01", "feed", failingSearch{}, nil) })
	skipped := func() []string {
		t.Helper()
		resp, err := h.localCl.Search(ctx, "xylophone", "", 0)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		var out []string
		for _, s := range resp.Skipped {
			out = append(out, s.Namespace+": "+s.Reason)
		}
		return out
	}

	got := skipped()
	far := localNodeID + "/geneva/pfail01: "
	if len(got) != 1 || !strings.HasPrefix(got[0], far) || !strings.Contains(got[0], "the index is not answering") {
		t.Fatalf("skipped = %q, want only the far plugin with its reason", got)
	}

	h.stopFarNode()
	got = skipped()
	if !slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, localNodeID+"/geneva: ") }) {
		t.Fatalf("skipped = %q, want the connection the far node went with", got)
	}
}
