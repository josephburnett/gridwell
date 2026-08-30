package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
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
