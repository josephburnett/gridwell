package server_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/namespace/namespacetest"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/sourcecache"
	"github.com/josephburnett/gridwell/internal/trace"
)

// unresolvedPages is a far plugin whose own source failed: it serves its page
// tile, and every page it is asked for answers Unavailable with its reason.
type unresolvedPages struct{ namespacetest.Unimplemented }

func (unresolvedPages) Info(context.Context, *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return &gridwellv1.InfoResponse{}, nil
}

func (unresolvedPages) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, send func(*gridwellv1.Event) error) error {
	if err := send(nil); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func (unresolvedPages) GetTile(_ context.Context, in *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	return &gridwellv1.TileResponse{Tile: &gridwellv1.Tile{Id: in.TileId, Kind: rpc.KindURL, W: 1, H: 1}}, nil
}

func (unresolvedPages) ServeContent(context.Context, *gridwellv1.ServeContentRequest, func(*gridwellv1.ServeContentChunk) error) error {
	return status.Error(codes.Unavailable, "hey: lookup app.hey.com: no such host")
}

// An Unavailable the far node answered is the far node speaking, so the
// connection stays light: no dark record, so no GridChanged and no prefetch
// walk on the next success. A far plugin's darkness, and a third node's behind
// the far node, are the far node's to publish. A far node that stops answering
// still reads dark. Across the real
// connection API and its dial, because whether the far node answered is the
// fact the hop carries.
func TestAnAnsweredUnavailableLeavesTheConnectionLight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	third := newFarNode(t, "cnode1")
	h := newFrontedTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil,
		func(ns namespace.Namespace) namespace.Namespace {
			cache, err := sourcecache.Open(t.TempDir() + "/cache.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cache.Close() })
			return cache.Front(ns, sourcecache.Options{Prefetch: true})
		},
		func(reg *plugin.Registry, _ *store.Store) { reg.Register("pheyab1", "hey", unresolvedPages{}, nil) },
		connectTo(t, "zurich", third, nil))
	root := "geneva/rnode1/" + h.rootBare
	if _, err := h.tClient.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	mark := traceSeq()
	for range 3 {
		err := h.tClient.ServeContent(ctx, &gridwellv1.ServeContentRequest{TileId: "geneva/pheyab1/~page"},
			func(*gridwellv1.ServeContentChunk) error { return nil })
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("ServeContent = %v, want the far plugin's Unavailable", err)
		}
		if _, err := h.tClient.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: root}); err != nil {
			t.Fatal(err)
		}
	}
	// The far node's own connection going unheard is its answer here.
	_ = third.api.Listener.Close()
	third.api.CloseClientConnections()
	if _, err := h.tClient.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: "geneva/rnode1/zurich/" + third.root}); status.Code(err) != codes.Unavailable {
		t.Fatalf("a grid behind the far node's dead connection = %v, want Unavailable", err)
	}
	for _, rec := range traceSince(mark) {
		if rec.KV["source"] == "geneva" && (rec.Kind == "dark" || rec.Kind == "prefetch") {
			t.Errorf("an answered Unavailable moved the connection: %s %v", rec.Msg, rec.KV)
		}
	}

	h.stopFarNode()
	mark = traceSeq()
	_ = h.tClient.ServeContent(ctx, &gridwellv1.ServeContentRequest{TileId: "geneva/pheyab1/~page"},
		func(*gridwellv1.ServeContentChunk) error { return nil })
	dark := false
	for _, rec := range traceSince(mark) {
		dark = dark || rec.Kind == "dark" && rec.KV["source"] == "geneva" && rec.KV["dark"] == "true"
	}
	if !dark {
		t.Error("a far node that stopped answering did not read dark")
	}
}

func traceSeq() uint64 {
	recs := trace.Default().Snapshot()
	if len(recs) == 0 {
		return 0
	}
	return recs[len(recs)-1].Seq
}

// traceSince is every sourcecache record since mark.
func traceSince(mark uint64) []tracewire.Record {
	var out []tracewire.Record
	for _, rec := range trace.Default().Snapshot() {
		if rec.Seq > mark && rec.Src == "sourcecache" {
			out = append(out, rec)
		}
	}
	return out
}
