package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/outbox"
)

// The persistence seam. The store's version rule
// and the client's outbox are each unit-tested on their own side; a unit test
// on each side of a contract cannot catch a mismatch, and the mismatch is the
// bug (CLAUDE.md §4). These tests run the REAL store through the REAL Connect
// handler with the REAL client, and then hand the result to the pure client
// packages that decide what happens next — clientsync.Of / React*, and
// client/outbox — so what the client will actually do is asserted against
// what the server actually answered.

// deadLink is a RoundTripper that fails every request while `down` is set —
// the wifi blip, at the one layer the client cannot distinguish from a dead
// server. Requests made while it is up go to the real transport.
type deadLink struct {
	inner http.RoundTripper
	down  atomic.Bool
	tries atomic.Int32
}

func (d *deadLink) RoundTrip(r *http.Request) (*http.Response, error) {
	d.tries.Add(1)
	if d.down.Load() {
		return nil, errors.New("simulated transport failure: no route to host")
	}
	return d.inner.RoundTrip(r)
}

// flakyClient returns a second client onto the same server whose link can be
// cut and restored, alongside a healthy client for assertions.
func flakyClient(t *testing.T) (hs *httptest.Server, healthy, flaky *rpc.Client, link *deadLink, root string) {
	t.Helper()
	hs, cl, root := newTestServer(t)
	inner := hs.Client()
	link = &deadLink{inner: inner.Transport}
	if link.inner == nil {
		link.inner = http.DefaultTransport
	}
	hc := *inner
	hc.Transport = link
	return hs, cl, rpc.NewClient(&hc, hs.URL, connect.WithProtoJSON()), link, root
}

// framingOf reads a doorway tile's persisted framing back out of the store,
// through the server — the far end of every framing write below.
func framingOf(t *testing.T, cl *rpc.Client, tileID string) rpc.Framing {
	t.Helper()
	tile, err := cl.GetTile(context.Background(), tileID)
	if err != nil {
		t.Fatalf("GetTile: %v", err)
	}
	return rpc.Framing{Cx: tile.ViewCx, Cy: tile.ViewCy, Zoom: tile.ViewZoom}
}

// TestContentConflictSurfaces (a): a text edit whose save basis lost a race
// comes back as a CONFLICT the user is shown, not as silence and not as an
// overwrite. The client's reaction table must say: surface it, refetch, and
// drop the local copy — the screen may not keep showing bytes the server
// refused.
func TestContentConflictSurfaces(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("v1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	basis := tile.Version

	// Someone else edits the bytes; our basis is now stale.
	if _, err := cl.WriteContent(ctx, tile.ID, basis, []byte("their edit")); err != nil {
		t.Fatalf("foreign edit: %v", err)
	}

	_, err = cl.WriteContent(ctx, tile.ID, basis, []byte("my edit"))
	if err == nil {
		t.Fatal("a stale save basis must not be accepted — that is the stomp")
	}
	if got := clientsync.Of(err); got != clientsync.OutcomeConflict {
		t.Fatalf("outcome = %v, want Conflict (code was %v)", got, errCode(err))
	}
	r := clientsync.ReactSave(clientsync.Of(err))
	if !r.Log || !r.Refetch || !r.DropLocal {
		t.Errorf("save conflict reaction = %+v, want it surfaced, refetched and reconciled", r)
	}

	// And the server still holds the other edit, byte-for-byte.
	body, _, _, err := cl.ReadContent(ctx, tile.ID)
	if err != nil || string(body) != "their edit" {
		t.Errorf("server holds %q (err %v), want the foreign edit intact", body, err)
	}
}

// TestCaptureDuringAnEditDoesNotConflict (b): the whole point of S5. A page
// title, a frozen face and a trail are captured onto the very tile the user
// is editing; the edit's save, claiming the basis it started from, must still
// land. Before, every capture bumped the row and turned the next keystroke's
// save into a conflict the user had to notice.
func TestCaptureDuringAnEditDoesNotConflict(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	tile, err := cl.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, URL: "https://start.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, basis, err := cl.ReadContent(ctx, tile.ID)
	if err != nil {
		t.Fatal(err)
	}

	// The live view is torn down mid-edit and freezes everything it saw.
	if _, err := cl.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: tile.ID,
		JPEG:   []byte("frozen frame"), URL: "https://start.example/deep",
		Title: "a title nobody typed", History: `["https://start.example"]`,
	}); err != nil {
		t.Fatalf("capture: %v", err)
	}
	// A shell-style automatic name capture on the same row, for good measure.
	if _, err := cl.SetContentZoom(ctx, &rpc.SetContentZoomRequest{
		TileID: tile.ID, ContentZoom: 1.25,
	}); err != nil {
		t.Fatalf("content zoom: %v", err)
	}

	if _, err := cl.WriteContent(ctx, tile.ID, basis, []byte("https://the.user.typed.this")); err != nil {
		t.Fatalf("the user's edit lost to a capture: %v", err)
	}
	after, err := cl.GetTile(ctx, tile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.URLString != "https://the.user.typed.this" {
		t.Errorf("address = %q, want the typed one", after.URLString)
	}
	// The capture is still there — last-writer-wins, not discarded.
	if after.URLHistory == "" || after.PreviewBlobID == 0 {
		t.Errorf("the capture was lost: history=%q preview=%d", after.URLHistory, after.PreviewBlobID)
	}
	// And exactly one bump happened, from the one user edit.
	if after.Version != basis+1 {
		t.Errorf("version %d -> %d; only the content edit may bump", basis, after.Version)
	}
}

// TestTransportFailureParksAndTheKickLandsIt (c): the server never spoke, so
// nothing may be dropped. The write parks in the one outbox; when the link
// returns, the retry kick's drain re-posts it through the same dispatcher and
// the value lands. Crossing the seam matters here: the outbox's fork keys on
// clientsync.Of over an error the REAL transport produced, not a constructed
// one.
func TestTransportFailureParksAndTheKickLandsIt(t *testing.T) {
	_, healthy, flaky, link, root := flakyClient(t)
	ctx := context.Background()

	well, err := healthy.CreateWell(ctx, &rpc.CreateWellRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}

	out := outbox.New()
	key := outbox.Key{Op: "SetFraming", ID: well.ID}
	framing := rpc.Framing{Cx: 12.5, Cy: -3.25, Zoom: 1.75}
	var post func()
	post = func() {
		_, err := flaky.SetFraming(ctx, &rpc.SetFramingRequest{TileID: well.ID, Framing: framing})
		out.Record(clientsync.Of(err), key, post)
	}

	link.down.Store(true)
	post()
	if out.Len() != 1 {
		t.Fatalf("a transport failure left %d writes parked, want 1", out.Len())
	}
	if got := framingOf(t, healthy, well.ID); got == framing {
		t.Fatal("the server took the write while the link was down")
	}

	// A drain against a STILL-dead link must converge, not lose the entry —
	// and it must actually reach the wire, not skip a key it thinks is
	// already in flight.
	before := link.tries.Load()
	for _, retry := range out.Drain() {
		retry()
	}
	if link.tries.Load() <= before {
		t.Error("the drain did not re-attempt the write")
	}
	if out.Len() != 1 {
		t.Fatalf("a failed retry left %d parked, want 1 (re-parked)", out.Len())
	}

	// The link returns; the retry kick drains.
	link.down.Store(false)
	for _, retry := range out.Drain() {
		retry()
	}
	if out.Len() != 0 {
		t.Errorf("a landed write left %d parked", out.Len())
	}
	if got := framingOf(t, healthy, well.ID); got != framing {
		t.Errorf("framing after the kick = %+v, want %+v", got, framing)
	}
}

// echoesOf drives two real content writes through the real handler and
// returns the two response rows alongside the two TileChanged rows the real
// Subscribe stream carried back, in the order the server emitted them. Both
// halves come off the wire: a constructed echo would only re-test the client's
// own unit rules.
func echoesOf(t *testing.T, cl *rpc.Client, tileID string, basis int64) (resp, echo [2]rpc.Tile) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	rows := make(chan rpc.Tile, 32)
	streamErr := make(chan error, 1)
	go func() {
		stream, err := cl.Subscribe(ctx)
		if err != nil {
			streamErr <- err
			return
		}
		defer stream.Close()
		for {
			ev, ok, err := stream.Recv()
			if err != nil || !ok {
				streamErr <- err
				return
			}
			if ev.Kind == rpc.EventTileChanged && ev.TileChanged != nil && ev.TileChanged.Tile.ID == tileID {
				rows <- ev.TileChanged.Tile
			}
		}
	}()
	time.Sleep(300 * time.Millisecond) // the stream is fanned in before the writes

	for i, body := range [][]byte{[]byte("first"), []byte("second")} {
		tile, err := cl.WriteContent(ctx, tileID, basis, body)
		if err != nil {
			t.Fatalf("write %d: %v", i+1, err)
		}
		resp[i] = *tile
		basis = tile.Version
	}
	// The stream also carries the create's own row, so collect by version
	// rather than by arrival position.
	seen := map[int64]rpc.Tile{}
	for {
		_, got1 := seen[resp[0].Version]
		_, got2 := seen[resp[1].Version]
		if got1 && got2 {
			return resp, [2]rpc.Tile{seen[resp[0].Version], seen[resp[1].Version]}
		}
		select {
		case row := <-rows:
			seen[row.Version] = row
		case err := <-streamErr:
			t.Fatalf("the subscribe stream ended: %v", err)
		case <-ctx.Done():
			t.Fatalf("only %d of the two echoes arrived", len(seen))
		}
	}
}

// TestEchoInterlockAcrossTheSeam (e): the interlock, over the real wire. Both
// sides are unit-tested — the store's version rule here, cache.Apply's
// "n.Version < cur.Version" drop in client/cache — and a unit test on each
// side of a contract cannot catch a mismatch (CLAUDE.md §4). What crosses the
// seam is a REAL response row and a REAL echo of the same write, which the
// client sees on two independent paths with no ordering between them. Feed
// them in every order those two paths can produce and the cached row must only
// ever move forward: a version that goes back is a tile the user watches roll
// back and then forward, a mutation nobody made.
//
// Three orderings are fixed, and everything else is free. Response 1 precedes
// response 2 and echo 2, because write 2's basis IS write 1's response row —
// it cannot even be sent before that lands. Echo 1 precedes echo 2, because
// one Subscribe stream is ordered. What is free is a response against the
// other write's echo, and that is the race the interlock exists for.
func TestEchoInterlockAcrossTheSeam(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	created, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("v0"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The seed is the room BEFORE the writes: that is what a client holding
	// this grid has when the first response or echo reaches it.
	grid, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	resp, echo := echoesOf(t, cl, created.ID, created.Version)
	if resp[0].Version >= resp[1].Version {
		t.Fatalf("two writes did not advance the row: %d then %d", resp[0].Version, resp[1].Version)
	}

	// A step is one arrival: the write's own response, applied the way
	// App.postWriteContent applies it, or its echo off the stream, applied
	// the way App.startSSE applies it.
	type step struct {
		name string
		tile rpc.Tile
		echo bool
	}
	r1 := step{"response 1", resp[0], false}
	r2 := step{"response 2", resp[1], false}
	e1 := step{"echo 1", echo[0], true}
	e2 := step{"echo 2", echo[1], true}

	for _, tc := range []struct {
		name  string
		order []step
	}{
		{"both responses before either echo", []step{r1, r2, e1, e2}},
		{"each response before its own echo", []step{r1, e1, r2, e2}},
		{"the second response trails both echoes", []step{r1, e1, e2, r2}},
		{"the first echo leads its own response", []step{e1, r1, r2, e2}},
		{"the first echo leads and the second response trails", []step{e1, r1, e2, r2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cache.New()
			c.PutGrid(grid.Grid, grid.Tiles)
			high := created.Version
			for _, s := range tc.order {
				if s.echo {
					c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: s.tile}})
				} else {
					c.UpdateTile(s.tile.GridID, s.tile)
				}
				g, ok := c.Grid(root)
				if !ok {
					t.Fatal("the grid left the cache")
				}
				got := g.Tiles[created.ID].Version
				if got < high {
					t.Fatalf("after %s the cached row went back to %d from %d", s.name, got, high)
				}
				high = got
			}
			g, _ := c.Grid(root)
			if got := g.Tiles[created.ID].Version; got != resp[1].Version {
				t.Errorf("settled at version %d, want the last write's %d", got, resp[1].Version)
			}
		})
	}
}

// TestAResponseRowSkipsTheInterlock pins what the case above deliberately does
// not reach, and it is a real hole: Cache.UpdateTile — the door
// App.postWriteContent puts a write RESPONSE through — writes the row with no
// version comparison at all, so an older response landing after a newer row
// DOES roll the tile back. It is unreachable today only because content saves
// for one tile are serialized by App.textSaves, so response N-1 cannot follow
// response N or the echo of N. That is a second row-writer holding the
// interlock's invariant by an ordering guarantee made three layers away, and
// it is owner-question 2 in docs/freshness.md ("Cache.UpdateTile is a second
// row-writer") — NOT decided here. This test asserts the CURRENT behaviour, so
// routing the response path through the interlock will fail it loudly and the
// decision gets made on purpose.
func TestAResponseRowSkipsTheInterlock(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	created, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("v0"),
	})
	if err != nil {
		t.Fatal(err)
	}
	grid, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	resp, echo := echoesOf(t, cl, created.ID, created.Version)

	c := cache.New()
	c.PutGrid(grid.Grid, grid.Tiles)
	// The newer write is settled, response and echo both.
	c.UpdateTile(resp[1].GridID, resp[1])
	c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: echo[1]}})
	// The older write's ECHO is refused, which is the interlock doing its job.
	c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: echo[0]}})
	g, _ := c.Grid(root)
	if got := g.Tiles[created.ID].Version; got != resp[1].Version {
		t.Fatalf("a stale echo moved the row to %d, want %d", got, resp[1].Version)
	}
	// The older write's RESPONSE is not: same fact, other door, no interlock.
	c.UpdateTile(resp[0].GridID, resp[0])
	g, _ = c.Grid(root)
	if got := g.Tiles[created.ID].Version; got != resp[0].Version {
		t.Fatalf("UpdateTile now guards the version (row at %d, the older response was %d) — "+
			"if that is the decision, freshness.md's owner-question 2 is answered and this test is the one to rewrite", got, resp[0].Version)
	}
}

// TestUnloadDrainsTheOutbox (d): a tab close is the other drain. A write
// parked by an earlier outage leaves through the BEACON transport — the one
// that survives the page — and lands in the store. Before S5 the unload flush
// knew only about fresh framing and dirty text, so anything an outage had
// parked died with the page.
func TestUnloadDrainsTheOutbox(t *testing.T) {
	hs, healthy, flaky, link, root := flakyClient(t)
	ctx := context.Background()

	well, err := healthy.CreateWell(ctx, &rpc.CreateWellRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	framing := rpc.Framing{Cx: 5, Cy: 6, Zoom: 0.75}
	req := &rpc.SetFramingRequest{TileID: well.ID, Framing: framing}

	out := outbox.New()
	key := outbox.Key{Op: "SetFraming", ID: well.ID}
	link.down.Store(true)
	_, err = flaky.SetFraming(ctx, req)
	out.Record(clientsync.Of(err), key, func() { t.Fatal("unused") })
	if out.Len() != 1 {
		t.Fatalf("parked %d, want 1", out.Len())
	}

	// beforeunload: the drain runs each entry through its beacon form.
	for range out.Drain() {
		path, body := rpc.SetFramingBeacon(req)
		if res := postBeacon(t, hs, path, rpc.BeaconJSONType, body); res.StatusCode != http.StatusOK {
			t.Fatalf("framing beacon = %d, want 200", res.StatusCode)
		}
	}
	if got := framingOf(t, healthy, well.ID); got != framing {
		t.Errorf("framing after the unload drain = %+v, want %+v", got, framing)
	}
}
