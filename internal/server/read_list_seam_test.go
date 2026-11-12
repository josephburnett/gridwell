package server_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
)

// A list read crosses the web API and two owners in one stream: a near body,
// a far one, a link through to the far one, a dead id and a refused one, each
// id's chunks contiguous and tagged as asked, a failed id answered by its
// verdict alone.
func TestReadListTagsEachIDsChunks(t *testing.T) {
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil)
	cl := h.localCl
	lp, err := cl.Handshake(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	near, far := rpc.HomeGrid(lp), farHome+"/"+h.rootBare
	ctx := context.Background()
	text := func(grid, body string) *gridwellv1.Tile {
		t.Helper()
		tl, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: grid, Tile: &gridwellv1.Tile{Kind: rpc.KindText, W: 1, H: 1}}, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return tl
	}
	mine := text(near, "near body")
	theirs := text(far, "far body")
	link, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: near, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 1, W: 1, H: 1, LinkTargetId: theirs.Id, AltText: "far"}})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{mine.Id, theirs.Id, link.Id, "zzzzzzz/12", "unqualified"}

	raw := gridwellv1connect.NewGridwellClient(h.localHTTP.Client(), h.localURL, connect.WithProtoJSON())
	stream, err := raw.ReadContent(ctx, connect.NewRequest(&gridwellv1.ReadContentRequest{TileIds: ids}))
	if err != nil {
		t.Fatal(err)
	}
	bodies, verdicts, ended := map[string]string{}, map[string]*gridwellv1.ReadVerdict{}, map[string]bool{}
	last := ""
	for stream.Receive() {
		c := stream.Msg()
		if c.TileId != last {
			if ended[c.TileId] {
				t.Errorf("%s's chunks are not contiguous", c.TileId)
			}
			ended[last], last = true, c.TileId
		}
		if c.Verdict != nil {
			verdicts[c.TileId] = c.Verdict
		}
		bodies[c.TileId] += string(c.Data)
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("the list read ended %v; a failed id is its own verdict, not the stream's", err)
	}
	want := map[string]string{mine.Id: "near body", theirs.Id: "far body", link.Id: "far body"}
	for id, body := range want {
		if bodies[id] != body || verdicts[id] != nil {
			t.Errorf("%s answered %q (%v), want %q", id, bodies[id], verdicts[id], body)
		}
	}
	dead := verdicts["zzzzzzz/12"]
	if dead == nil || connect.Code(dead.Code) != connect.CodeNotFound || len(dead.Details) != 1 ||
		dead.Details[0].TypeUrl != "type.googleapis.com/gridwell.v1.DeadLink" {
		t.Errorf("the undeclared id's verdict = %v, want NotFound carrying the dead verdict", dead)
	}
	if v := verdicts["unqualified"]; v == nil || connect.Code(v.Code) != connect.CodeInvalidArgument {
		t.Errorf("the unqualified id's verdict = %v, want InvalidArgument", v)
	}
}

// A request names one id or a list, never both or neither.
func TestReadContentNamesOneOrAList(t *testing.T) {
	h := newTransportHarness(t, nil, nil)
	raw := gridwellv1connect.NewGridwellClient(h.localHTTP.Client(), h.localURL, connect.WithProtoJSON())
	for name, req := range map[string]*gridwellv1.ReadContentRequest{
		"neither": {},
		"both":    {TileId: "zzzzzzz/1", TileIds: []string{"zzzzzzz/2"}},
	} {
		stream, err := raw.ReadContent(context.Background(), connect.NewRequest(req))
		if err == nil {
			for stream.Receive() {
				t.Errorf("%s: answered a chunk", name)
			}
			err = stream.Err()
		}
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v, want InvalidArgument", name, err)
		}
	}
}
