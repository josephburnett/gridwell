package server

import (
	"net/http"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A box's link carries none of its thread's content facts, so a client that
// has only the box reads the thread by the link's target, the one hop
// contentrow.Ask names. The node must answer that read from a context nobody
// has listed yet, through the browser door, with the row the content door
// then serves at the target's address.
func TestHeyLinkTargetReadsWithItsContextUnlisted(t *testing.T) {
	hs, _, info := heyStack(t, heyAccount(t))
	cl := gridwellv1connect.NewGridwellClient(hs.Client(), hs.URL)
	ctx := t.Context()

	aside, err := cl.GetGrid(ctx, connect.NewRequest(&gridwellv1.GetGridRequest{
		GridId: heyNS + "/" + heyGrids(t, info)["set aside"]}))
	if err != nil {
		t.Fatal(err)
	}
	link := tileWithLabel(t, aside.Msg, "Lease renewal")
	if link.LinkTargetId == "" || !link.Reference {
		t.Fatalf("the Set Aside row is no link: %v", link)
	}
	if link.ServesPage || link.UrlString != "" {
		t.Errorf("the link carries its target's content facts: %v", link)
	}

	got, err := cl.GetTile(ctx, connect.NewRequest(&gridwellv1.GetTileRequest{TileId: link.LinkTargetId}))
	if err != nil {
		t.Fatalf("GetTile(%s): %v", link.LinkTargetId, err)
	}
	target := got.Msg.Tile
	if target.Id != link.LinkTargetId || !rpc.PageContent(target) {
		t.Fatalf("the target = %v, want the served page %s", target, link.LinkTargetId)
	}

	res, body := get(t, hs.Client(), rpc.PageURL(hs.URL, ContentToken(testPassword), target.Id), "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET the target's page = %d %q", res.StatusCode, body)
	}
}
