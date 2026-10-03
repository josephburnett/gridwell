package server

// Every verb the hey plugin serves, on the shipped binary over heyfake and
// through the node's adapter (docs/plugin-standard.md rule 16). Listing and
// the email page are hey_seam_test.go's; these are the rest.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// heyGrids is the plugin's collections by menu label, everything included.
func heyGrids(t *testing.T, info *gridwellv1.InfoResponse) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range info.MenuEntries {
		out[m.Label] = m.GridId
	}
	if len(out) != 4 {
		t.Fatalf("menu entries = %v", info.MenuEntries)
	}
	return out
}

// contextOf is the plugin context a grid id addresses.
func contextOf(t *testing.T, gridID string) string {
	t.Helper()
	c, _, isTile, ok := rpc.SplitEntryID(gridID)
	if !ok || isTile {
		t.Fatalf("%q is no entry grid", gridID)
	}
	return c
}

// keyOf is the plugin key a tile id addresses.
func keyOf(t *testing.T, tileID string) string {
	t.Helper()
	_, k, isTile, ok := rpc.SplitEntryID(tileID)
	if !ok || !isTile {
		t.Fatalf("%q is no entry tile", tileID)
	}
	return k
}

// walked reads everything, which waits for a walk of every box, and answers
// it.
func walked(t *testing.T, cl namespace.Namespace, grids map[string]string) *gridwellv1.GetGridResponse {
	t.Helper()
	g, err := cl.GetGrid(t.Context(), &gridwellv1.GetGridRequest{GridId: grids["everything"]})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// Probe answers for the context the tile id names: a thread that is in Set
// Aside is there, is not in the Imbox, and is in everything. Answering for
// the plugin as a whole would retire a box's link to a thread that only moved
// to another box, or keep one that left.
func TestHeyProbeAnswersForTheContextAsked(t *testing.T) {
	_, cl, info := heyStack(t, heyAccount(t))
	grids := heyGrids(t, info)
	walked(t, cl, grids)
	aside, err := cl.GetGrid(t.Context(), &gridwellv1.GetGridRequest{GridId: grids["set aside"]})
	if err != nil {
		t.Fatal(err)
	}
	key := keyOf(t, tileWithLabel(t, aside, "Lease renewal").Id)

	for _, c := range []struct {
		grid string
		want gridwellv1.ProbeResponse_Presence
	}{
		{"set aside", gridwellv1.ProbeResponse_PRESENCE_PRESENT},
		{"imbox", gridwellv1.ProbeResponse_PRESENCE_GONE},
		{"everything", gridwellv1.ProbeResponse_PRESENCE_PRESENT},
	} {
		id := rpc.EntryTileID(contextOf(t, grids[c.grid]), key)
		pr, err := cl.Probe(t.Context(), &gridwellv1.ProbeRequest{TileId: id})
		if err != nil {
			t.Fatalf("Probe in %s: %v", c.grid, err)
		}
		if pr.Presence != c.want {
			t.Errorf("Probe in %s = %v, want %v", c.grid, pr.Presence, c.want)
		}
	}
}

// A search result is the thread's one tile, in everything, wherever the
// thread is filed, so opening it opens the same tile a box links to.
func TestHeySearchLandsOnTheEverythingTile(t *testing.T) {
	_, cl, info := heyStack(t, heyAccount(t))
	grids := heyGrids(t, info)
	all := walked(t, cl, grids)
	want := tileWithLabel(t, all, "Conference talk")

	resp, err := cl.Search(t.Context(), &gridwellv1.SearchRequest{Query: "conference"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %v, want the one thread", resp.Results)
	}
	if got := resp.Results[0].Tile; got.Id != want.Id || got.GridId != grids["everything"] {
		t.Errorf("result = %s in %s, want %s in %s", got.Id, got.GridId, want.Id, grids["everything"])
	}
}

// The trash gesture on an email is refused with the reason, and the tile
// stays: a refusal the user cannot read looks like a delete that failed to
// stick.
func TestHeyDeleteIsRefusedWithTheReason(t *testing.T) {
	_, cl, info := heyStack(t, heyAccount(t))
	grids := heyGrids(t, info)
	email := tileWithLabel(t, walked(t, cl, grids), "Lunch plans")

	_, err := cl.DeleteTile(t.Context(), &gridwellv1.DeleteTileRequest{TileId: email.Id})
	if status.Code(err) != codes.Unimplemented || !strings.Contains(err.Error(), "read-only projection of HEY") {
		t.Fatalf("delete = %v, want Unimplemented with the reason", err)
	}
	tileWithLabel(t, walked(t, cl, grids), "Lunch plans")
}

// Showing a collection opens the plugin's Watch, and the stream counts open at
// its header: the node then checks the shown grid and tells the client. With
// the feed silent and nothing read, the header is the only thing the plugin
// sends, so a stream that waits for its first change never opens and the
// client hears nothing.
func TestHeyWatchHeaderArrivesWhenAStreamOpens(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cp := plugintest.Spawn(t, "hey", heyAccount(t).Config(nil))
	a, stop := pluginhost.Start(cp, st.Namespace(heyNS), nil, "plugin "+heyNS+" watch")
	reg := plugin.NewRegistry()
	reg.Register(heyNS, "hey", a, stop)
	hs := serveWeb(t, mustNew(t, reg, Config{}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var shown string
	for _, p := range lp.Plugins {
		for _, m := range p.MenuEntries {
			if p.Uuid == heyNS && m.Label == "imbox" {
				shown = m.GridId
			}
		}
	}
	if shown == "" {
		t.Fatal("no imbox in the handshake")
	}

	events := make(chan *gridwellv1.Event, 64)
	go func() {
		es, err := cl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer es.Close()
		for {
			ev, ok, err := es.Recv()
			if err != nil || !ok {
				return
			}
			events <- ev
		}
	}()
	if err := cl.SetInterest(ctx, []string{shown}); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case ev := <-events:
			if ev.GetGridChanged().GetGridId() == shown {
				return
			}
		case <-ctx.Done():
			t.Fatalf("no change to %s reached the client", shown)
		}
	}
}
